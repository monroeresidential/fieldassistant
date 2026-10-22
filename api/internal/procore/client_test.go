package procore

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// recordedRequest is what the fake Procore server saw for one request.
type recordedRequest struct {
	Path   string
	Query  map[string]string
	Header http.Header
}

// fakeProcore is an httptest server routing by path to per-test handlers.
type fakeProcore struct {
	t        *testing.T
	srv      *httptest.Server
	mu       sync.Mutex
	requests []recordedRequest
}

func newFakeProcore(t *testing.T, handlers map[string]http.HandlerFunc) *fakeProcore {
	t.Helper()
	f := &fakeProcore{t: t}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := map[string]string{}
		for k := range r.URL.Query() {
			q[k] = r.URL.Query().Get(k)
		}
		f.mu.Lock()
		f.requests = append(f.requests, recordedRequest{Path: r.URL.Path, Query: q, Header: r.Header.Clone()})
		f.mu.Unlock()
		h, ok := handlers[r.URL.Path]
		if !ok {
			t.Errorf("unexpected request path %q", r.URL.Path)
			http.NotFound(w, r)
			return
		}
		h(w, r)
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeProcore) recorded() []recordedRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]recordedRequest(nil), f.requests...)
}

// sleepRecorder records requested sleeps without sleeping.
type sleepRecorder struct {
	mu    sync.Mutex
	calls []time.Duration
}

func (s *sleepRecorder) sleep(d time.Duration) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls = append(s.calls, d)
}

func (s *sleepRecorder) recorded() []time.Duration {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]time.Duration(nil), s.calls...)
}

var clientNow = time.Unix(1_800_000_000, 0)

func newTestClient(f *fakeProcore, s *sleepRecorder) *Client {
	return NewClient(f.srv.URL, f.srv.Client(), s.sleep, func() time.Time { return clientNow })
}

func writeJSON(w http.ResponseWriter, status int, body string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write([]byte(body))
}

// itemsJSON returns a JSON array of n objects with ids starting at first.
func itemsJSON(first, n int) string {
	parts := make([]string, n)
	for i := range n {
		parts[i] = fmt.Sprintf(`{"id":%d,"name":"P%d","display_name":"Project %d"}`, first+i, first+i, first+i)
	}
	return "[" + strings.Join(parts, ",") + "]"
}

func TestClient_Me_SendsBearer(t *testing.T) {
	f := newFakeProcore(t, map[string]http.HandlerFunc{
		"/rest/v1.0/me": func(w http.ResponseWriter, r *http.Request) {
			writeJSON(w, 200, `{"id":42,"login":"drew@example.com","name":"Drew"}`)
		},
	})
	s := &sleepRecorder{}
	me, err := newTestClient(f, s).Me(context.Background(), "tok-abc")
	if err != nil {
		t.Fatalf("Me: %v", err)
	}
	if me != (Me{ID: 42, Login: "drew@example.com", Name: "Drew"}) {
		t.Fatalf("Me = %+v", me)
	}
	reqs := f.recorded()
	if len(reqs) != 1 {
		t.Fatalf("requests = %d, want 1", len(reqs))
	}
	if got := reqs[0].Header.Get("Authorization"); got != "Bearer tok-abc" {
		t.Errorf("Authorization = %q", got)
	}
	if got := reqs[0].Header.Get("Accept"); got != "application/json" {
		t.Errorf("Accept = %q", got)
	}
	if got := reqs[0].Header.Get("Procore-Company-Id"); got != "" {
		t.Errorf("Me sent Procore-Company-Id = %q", got)
	}
	if len(s.recorded()) != 0 {
		t.Errorf("unexpected sleeps %v", s.recorded())
	}
}

func TestClient_ListCompanies_RetriesOn429(t *testing.T) {
	calls := 0
	f := newFakeProcore(t, map[string]http.HandlerFunc{
		"/rest/v1.0/companies": func(w http.ResponseWriter, r *http.Request) {
			calls++
			if calls == 1 {
				w.Header().Set("X-Rate-Limit-Reset", strconv.FormatInt(clientNow.Add(2*time.Second).Unix(), 10))
				writeJSON(w, http.StatusTooManyRequests, `{"error":"rate limited"}`)
				return
			}
			writeJSON(w, 200, `[{"id":7,"name":"Monroe"},{"id":8,"name":"Other"}]`)
		},
	})
	s := &sleepRecorder{}
	got, err := newTestClient(f, s).ListCompanies(context.Background(), "tok")
	if err != nil {
		t.Fatalf("ListCompanies: %v", err)
	}
	want := []Company{{ID: 7, Name: "Monroe"}, {ID: 8, Name: "Other"}}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("companies = %+v", got)
	}
	if sl := s.recorded(); len(sl) != 1 || sl[0] != 2*time.Second {
		t.Fatalf("sleeps = %v, want [2s]", sl)
	}
	reqs := f.recorded()
	if len(reqs) != 2 {
		t.Fatalf("requests = %d, want 2", len(reqs))
	}
	for i, r := range reqs {
		if got := r.Header.Get("Procore-Company-Id"); got != "" {
			t.Errorf("request %d sent Procore-Company-Id = %q", i, got)
		}
	}
}

func TestClient_429_WaitDefaultsAndCaps(t *testing.T) {
	cases := []struct {
		name   string
		header string
		want   time.Duration
	}{
		{"missing", "", time.Second},
		{"unparsable", "soon", time.Second},
		{"far future capped", strconv.FormatInt(clientNow.Add(10*time.Minute).Unix(), 10), 60 * time.Second},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			f := newFakeProcore(t, map[string]http.HandlerFunc{
				"/rest/v1.0/me": func(w http.ResponseWriter, r *http.Request) {
					calls++
					if calls == 1 {
						if tc.header != "" {
							w.Header().Set("X-Rate-Limit-Reset", tc.header)
						}
						writeJSON(w, http.StatusTooManyRequests, `{}`)
						return
					}
					writeJSON(w, 200, `{"id":1}`)
				},
			})
			s := &sleepRecorder{}
			if _, err := newTestClient(f, s).Me(context.Background(), "tok"); err != nil {
				t.Fatalf("Me: %v", err)
			}
			if sl := s.recorded(); len(sl) != 1 || sl[0] != tc.want {
				t.Fatalf("sleeps = %v, want [%v]", sl, tc.want)
			}
		})
	}
}

func TestClient_429_GivesUpAfterThreeRetries(t *testing.T) {
	f := newFakeProcore(t, map[string]http.HandlerFunc{
		"/rest/v1.0/me": func(w http.ResponseWriter, r *http.Request) {
			writeJSON(w, http.StatusTooManyRequests, `{"error":"slow down"}`)
		},
	})
	s := &sleepRecorder{}
	_, err := newTestClient(f, s).Me(context.Background(), "tok")
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.Status != http.StatusTooManyRequests {
		t.Fatalf("err = %v, want *APIError 429", err)
	}
	if n := len(f.recorded()); n != 4 {
		t.Errorf("requests = %d, want 4 (1 + 3 retries)", n)
	}
	if n := len(s.recorded()); n != 3 {
		t.Errorf("sleeps = %d, want 3", n)
	}
}

func TestClient_ListProjects_FollowsPagination(t *testing.T) {
	f := newFakeProcore(t, map[string]http.HandlerFunc{
		"/rest/v1.0/projects": func(w http.ResponseWriter, r *http.Request) {
			switch r.URL.Query().Get("page") {
			case "1":
				writeJSON(w, 200, itemsJSON(1, 100))
			case "2":
				writeJSON(w, 200, itemsJSON(101, 3))
			default:
				t.Errorf("unexpected page %q", r.URL.Query().Get("page"))
				writeJSON(w, 200, `[]`)
			}
		},
	})
	s := &sleepRecorder{}
	got, err := newTestClient(f, s).ListProjects(context.Background(), "tok", 7)
	if err != nil {
		t.Fatalf("ListProjects: %v", err)
	}
	if len(got) != 103 {
		t.Fatalf("len = %d, want 103", len(got))
	}
	if got[102] != (Project{ID: 103, Name: "P103", DisplayName: "Project 103"}) {
		t.Errorf("last project = %+v", got[102])
	}
	reqs := f.recorded()
	if len(reqs) != 2 {
		t.Fatalf("requests = %d, want 2", len(reqs))
	}
	for i, r := range reqs {
		if got := r.Header.Get("Procore-Company-Id"); got != "7" {
			t.Errorf("request %d Procore-Company-Id = %q, want 7", i, got)
		}
		wantQ := map[string]string{"company_id": "7", "per_page": "100", "page": strconv.Itoa(i + 1)}
		for k, v := range wantQ {
			if r.Query[k] != v {
				t.Errorf("request %d query %s = %q, want %q", i, k, r.Query[k], v)
			}
		}
	}
}

func TestClient_Pagination_CapsAtFiftyPages(t *testing.T) {
	f := newFakeProcore(t, map[string]http.HandlerFunc{
		"/rest/v1.0/projects": func(w http.ResponseWriter, r *http.Request) {
			writeJSON(w, 200, itemsJSON(1, 100))
		},
	})
	_, err := newTestClient(f, &sleepRecorder{}).ListProjects(context.Background(), "tok", 7)
	if err == nil {
		t.Fatal("err = nil, want page-limit error")
	}
	if n := len(f.recorded()); n != 50 {
		t.Errorf("requests = %d, want 50", n)
	}
}

func TestClient_ListRFIs_DecodesNumberForms(t *testing.T) {
	f := newFakeProcore(t, map[string]http.HandlerFunc{
		"/rest/v1.0/projects/55/rfis": func(w http.ResponseWriter, r *http.Request) {
			writeJSON(w, 200, `[
				{"id":1,"number":12,"subject":"Beam size","status":"open","due_date":"2026-11-01","assignees":[{"id":9,"name":"Pat"}]},
				{"id":2,"number":"A-3","subject":"Door","status":"closed","due_date":null,"assignees":[]}
			]`)
		},
	})
	got, err := newTestClient(f, &sleepRecorder{}).ListRFIs(context.Background(), "tok", 7, 55)
	if err != nil {
		t.Fatalf("ListRFIs: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("len = %d, want 2", len(got))
	}
	if got[0].Number != "12" || got[1].Number != "A-3" {
		t.Errorf("numbers = %q, %q; want 12, A-3", got[0].Number, got[1].Number)
	}
	if got[0].DueDate == nil || *got[0].DueDate != "2026-11-01" || got[1].DueDate != nil {
		t.Errorf("due dates = %v, %v", got[0].DueDate, got[1].DueDate)
	}
	if len(got[0].Assignees) != 1 || got[0].Assignees[0] != (Person{ID: 9, Name: "Pat"}) {
		t.Errorf("assignees = %+v", got[0].Assignees)
	}
	if got[0].Subject != "Beam size" || got[0].Status != "open" {
		t.Errorf("rfi[0] = %+v", got[0])
	}
	reqs := f.recorded()
	if len(reqs) != 1 {
		t.Fatalf("requests = %d, want 1", len(reqs))
	}
	r := reqs[0]
	if r.Header.Get("Procore-Company-Id") != "7" || r.Query["per_page"] != "100" || r.Query["page"] != "1" {
		t.Errorf("request header/query = %q %v", r.Header.Get("Procore-Company-Id"), r.Query)
	}
}

func TestClient_ListRFIs_403_IsErrPermissionDenied(t *testing.T) {
	f := newFakeProcore(t, map[string]http.HandlerFunc{
		"/rest/v1.0/projects/55/rfis": func(w http.ResponseWriter, r *http.Request) {
			writeJSON(w, http.StatusForbidden, `{"errors":"forbidden"}`)
		},
	})
	_, err := newTestClient(f, &sleepRecorder{}).ListRFIs(context.Background(), "secret-token", 7, 55)
	if !errors.Is(err, ErrPermissionDenied) {
		t.Fatalf("err = %v, want ErrPermissionDenied", err)
	}
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.Status != http.StatusForbidden || !strings.Contains(apiErr.Body, "forbidden") {
		t.Fatalf("errors.As APIError = %+v", apiErr)
	}
	if strings.Contains(err.Error(), "secret-token") {
		t.Errorf("error leaks token: %v", err)
	}
}

func TestClient_401_IsErrUnauthorized(t *testing.T) {
	f := newFakeProcore(t, map[string]http.HandlerFunc{
		"/rest/v1.0/me": func(w http.ResponseWriter, r *http.Request) {
			writeJSON(w, http.StatusUnauthorized, `{"error":"invalid_token"}`)
		},
	})
	_, err := newTestClient(f, &sleepRecorder{}).Me(context.Background(), "secret-token")
	if !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("err = %v, want ErrUnauthorized", err)
	}
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.Status != http.StatusUnauthorized {
		t.Fatalf("errors.As APIError = %+v", apiErr)
	}
	if strings.Contains(err.Error(), "secret-token") {
		t.Errorf("error leaks token: %v", err)
	}
}

func TestClient_OtherStatus_IsAPIErrorWithTruncatedBody(t *testing.T) {
	f := newFakeProcore(t, map[string]http.HandlerFunc{
		"/rest/v1.0/companies": func(w http.ResponseWriter, r *http.Request) {
			writeJSON(w, http.StatusInternalServerError, strings.Repeat("x", 1000))
		},
	})
	_, err := newTestClient(f, &sleepRecorder{}).ListCompanies(context.Background(), "tok")
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.Status != 500 {
		t.Fatalf("err = %v, want *APIError 500", err)
	}
	if len(apiErr.Body) != maxErrBody {
		t.Errorf("body len = %d, want %d", len(apiErr.Body), maxErrBody)
	}
	if errors.Is(err, ErrUnauthorized) || errors.Is(err, ErrPermissionDenied) {
		t.Errorf("500 mapped to a sentinel: %v", err)
	}
}
