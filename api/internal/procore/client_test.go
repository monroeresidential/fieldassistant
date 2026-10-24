package procore

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
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
				{"id":1,"number":12,"subject":"Beam size","status":"open","due_date":"2026-11-01","assignees":[{"id":9,"name":"Pat"}],"created_at":"2026-10-24T15:04:05Z","created_by":{"id":42,"name":"Pat Field","login":"pat@example.com"}},
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
	if got[0].CreatedAt != "2026-10-24T15:04:05Z" || got[0].CreatedBy != (Person{ID: 42, Name: "Pat Field"}) {
		t.Errorf("created = %q by %+v", got[0].CreatedAt, got[0].CreatedBy)
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

func TestClient_ListCompanies_FollowsPagination(t *testing.T) {
	f := newFakeProcore(t, map[string]http.HandlerFunc{
		"/rest/v1.0/companies": func(w http.ResponseWriter, r *http.Request) {
			switch r.URL.Query().Get("page") {
			case "1":
				writeJSON(w, 200, itemsJSON(1, 100))
			case "2":
				writeJSON(w, 200, itemsJSON(101, 2))
			default:
				t.Errorf("unexpected page %q", r.URL.Query().Get("page"))
				writeJSON(w, 200, `[]`)
			}
		},
	})
	got, err := newTestClient(f, &sleepRecorder{}).ListCompanies(context.Background(), "tok")
	if err != nil {
		t.Fatalf("ListCompanies: %v", err)
	}
	if len(got) != 102 {
		t.Fatalf("len = %d, want 102", len(got))
	}
	if got[101] != (Company{ID: 102, Name: "P102"}) {
		t.Errorf("last company = %+v", got[101])
	}
	reqs := f.recorded()
	if len(reqs) != 2 {
		t.Fatalf("requests = %d, want 2", len(reqs))
	}
	for i, r := range reqs {
		if got := r.Header.Get("Procore-Company-Id"); got != "" {
			t.Errorf("request %d sent Procore-Company-Id = %q", i, got)
		}
		if r.Query["per_page"] != "100" || r.Query["page"] != strconv.Itoa(i+1) {
			t.Errorf("request %d query = %v", i, r.Query)
		}
	}
}

func TestClient_NetworkError_IsErrUnavailable(t *testing.T) {
	f := newFakeProcore(t, map[string]http.HandlerFunc{})
	c := newTestClient(f, &sleepRecorder{})
	f.srv.Close()

	_, err := c.ListCompanies(context.Background(), "secret-token")
	if !errors.Is(err, ErrUnavailable) {
		t.Fatalf("err = %v, want ErrUnavailable", err)
	}
	if strings.Contains(err.Error(), "secret-token") {
		t.Errorf("error leaks token: %v", err)
	}
}

func TestClient_BadJSON_IsErrUnavailable(t *testing.T) {
	f := newFakeProcore(t, map[string]http.HandlerFunc{
		"/rest/v1.0/me": func(w http.ResponseWriter, r *http.Request) {
			writeJSON(w, http.StatusOK, `{"id":`)
		},
	})
	_, err := newTestClient(f, &sleepRecorder{}).Me(context.Background(), "secret-token")
	if !errors.Is(err, ErrUnavailable) {
		t.Fatalf("err = %v, want ErrUnavailable", err)
	}
}

func TestClient_CallerCancelled_IsNotErrUnavailable(t *testing.T) {
	f := newFakeProcore(t, map[string]http.HandlerFunc{})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := newTestClient(f, &sleepRecorder{}).Me(ctx, "secret-token")
	if !errors.Is(err, context.Canceled) || errors.Is(err, ErrUnavailable) {
		t.Fatalf("err = %v, want context.Canceled and not ErrUnavailable", err)
	}
}

func TestClient_ListProjectUsers_PathAndCompanyHeader(t *testing.T) {
	f := newFakeProcore(t, map[string]http.HandlerFunc{
		"/rest/v1.0/projects/9/users": func(w http.ResponseWriter, r *http.Request) {
			writeJSON(w, 200, `[{"id":3,"name":"Pat","email_address":"pat@x.com","job_title":"PM","vendor":{"id":5,"name":"Acme"}}]`)
		},
	})
	got, err := newTestClient(f, &sleepRecorder{}).ListProjectUsers(context.Background(), "tok", 7, 9)
	if err != nil {
		t.Fatalf("ListProjectUsers: %v", err)
	}
	if len(got) != 1 || got[0].ID != 3 || got[0].EmailAddress != "pat@x.com" || got[0].JobTitle != "PM" || got[0].Vendor.Name != "Acme" || got[0].Vendor.ID != 5 {
		t.Fatalf("users = %+v", got)
	}
	reqs := f.recorded()
	if len(reqs) != 1 || reqs[0].Header.Get("Procore-Company-Id") != "7" {
		t.Fatalf("requests = %+v", reqs)
	}
}

func TestClient_ListLocationsSpecsVendors_DecodeFields(t *testing.T) {
	f := newFakeProcore(t, map[string]http.HandlerFunc{
		"/rest/v1.0/projects/9/locations": func(w http.ResponseWriter, r *http.Request) {
			writeJSON(w, 200, `[{"id":1,"name":"L1","node_name":"Bldg > L1","parent_id":null},{"id":2,"name":"R2","node_name":"Bldg > L1 > R2","parent_id":1}]`)
		},
		"/rest/v1.0/projects/9/specification_sections": func(w http.ResponseWriter, r *http.Request) {
			writeJSON(w, 200, `[{"id":4,"number":"03 30 00","description":"Concrete","label":"03 30 00 - Concrete"}]`)
		},
		"/rest/v1.0/projects/9/vendors": func(w http.ResponseWriter, r *http.Request) {
			writeJSON(w, 200, `[{"id":6,"name":"Acme Inc","trade_name":"Acme","abbreviated_name":"AC"}]`)
		},
	})
	c := newTestClient(f, &sleepRecorder{})
	ctx := context.Background()
	locs, err := c.ListLocations(ctx, "tok", 7, 9)
	if err != nil || len(locs) != 2 || locs[0].ParentID != nil || locs[1].ParentID == nil || *locs[1].ParentID != 1 || locs[1].NodeName != "Bldg > L1 > R2" {
		t.Fatalf("locations = %+v, %v", locs, err)
	}
	specs, err := c.ListSpecSections(ctx, "tok", 7, 9)
	if err != nil || len(specs) != 1 || specs[0].Number != "03 30 00" || specs[0].Label != "03 30 00 - Concrete" || specs[0].Description != "Concrete" {
		t.Fatalf("specs = %+v, %v", specs, err)
	}
	vendors, err := c.ListVendors(ctx, "tok", 7, 9)
	if err != nil || len(vendors) != 1 || vendors[0].TradeName != "Acme" || vendors[0].AbbreviatedName != "AC" {
		t.Fatalf("vendors = %+v, %v", vendors, err)
	}
}

func TestClient_ListCostCodes_UsesQueryParam(t *testing.T) {
	f := newFakeProcore(t, map[string]http.HandlerFunc{
		"/rest/v1.0/cost_codes": func(w http.ResponseWriter, r *http.Request) {
			writeJSON(w, 200, `[{"id":8,"full_code":"03-300","name":"Cast-in-place"}]`)
		},
	})
	got, err := newTestClient(f, &sleepRecorder{}).ListCostCodes(context.Background(), "tok", 7, 9)
	if err != nil || len(got) != 1 || got[0].FullCode != "03-300" || got[0].Name != "Cast-in-place" {
		t.Fatalf("cost codes = %+v, %v", got, err)
	}
	r := f.recorded()[0]
	if r.Query["project_id"] != "9" || r.Header.Get("Procore-Company-Id") != "7" {
		t.Errorf("query/header = %v %q", r.Query, r.Header.Get("Procore-Company-Id"))
	}
}

func TestClient_ProbeKind_OnePageOfOne(t *testing.T) {
	f := newFakeProcore(t, map[string]http.HandlerFunc{
		"/rest/v1.0/projects/9/users": func(w http.ResponseWriter, r *http.Request) {
			writeJSON(w, 200, `[{"id":1,"name":"Ann"}]`)
		},
		"/rest/v1.0/cost_codes": func(w http.ResponseWriter, r *http.Request) {
			writeJSON(w, 403, `{"errors":"no budget access"}`)
		},
	})
	c := newTestClient(f, &sleepRecorder{})
	ctx := context.Background()
	if err := c.ProbeKind(ctx, "tok", 7, 9, KindUsers); err != nil {
		t.Fatalf("ProbeKind users = %v", err)
	}
	r := f.recorded()[0]
	if r.Query["per_page"] != "1" || r.Query["page"] != "1" || r.Header.Get("Procore-Company-Id") != "7" {
		t.Errorf("probe request = %+v", r)
	}
	if err := c.ProbeKind(ctx, "tok", 7, 9, KindCostCodes); !errors.Is(err, ErrPermissionDenied) {
		t.Fatalf("ProbeKind cost_codes = %v, want ErrPermissionDenied", err)
	}
	if r := f.recorded()[1]; r.Query["project_id"] != "9" || r.Query["per_page"] != "1" {
		t.Errorf("cost code probe query = %v", r.Query)
	}
	if err := c.ProbeKind(ctx, "tok", 7, 9, "nope"); err == nil {
		t.Error("ProbeKind of an unknown kind succeeded")
	}
}

func TestClient_ListDrawings_V11(t *testing.T) {
	f := newFakeProcore(t, map[string]http.HandlerFunc{
		"/rest/v1.1/drawings": func(w http.ResponseWriter, r *http.Request) {
			writeJSON(w, 200, `[{"id":2,"number":"A-101","title":"Plan","discipline":"Architectural"}]`)
		},
	})
	got, err := newTestClient(f, &sleepRecorder{}).ListDrawings(context.Background(), "tok", 7, 9)
	if err != nil || len(got) != 1 || got[0] != (Drawing{ID: 2, Number: "A-101", Title: "Plan", Discipline: "Architectural"}) {
		t.Fatalf("drawings = %+v, %v", got, err)
	}
	if r := f.recorded()[0]; r.Query["project_id"] != "9" {
		t.Errorf("query = %v", r.Query)
	}
}

func TestClient_GetProject_TimeZone(t *testing.T) {
	f := newFakeProcore(t, map[string]http.HandlerFunc{
		"/rest/v1.0/projects/9": func(w http.ResponseWriter, r *http.Request) {
			writeJSON(w, 200, `{"id":9,"name":"Tower","display_name":"Tower One","time_zone":"America/Chicago"}`)
		},
	})
	got, err := newTestClient(f, &sleepRecorder{}).GetProject(context.Background(), "tok", 7, 9)
	if err != nil {
		t.Fatalf("GetProject: %v", err)
	}
	if got != (ProjectDetail{ID: 9, Name: "Tower", DisplayName: "Tower One", TimeZone: "America/Chicago"}) {
		t.Fatalf("project = %+v", got)
	}
	if h := f.recorded()[0].Header.Get("Procore-Company-Id"); h != "7" {
		t.Errorf("company header = %q", h)
	}
	// Procore's Show Project endpoint answers 400 "Missing Project or Company
	// ID" without company_id in the query, header or not.
	if q := f.recorded()[0].Query["company_id"]; q != "7" {
		t.Errorf("company_id query = %q, want 7", q)
	}
}

func TestClient_CreateRFI_PostsWrappedBodyWithValidations(t *testing.T) {
	var method, ctype string
	var raw []byte
	f := newFakeProcore(t, map[string]http.HandlerFunc{
		"/rest/v1.0/projects/9/rfis": func(w http.ResponseWriter, r *http.Request) {
			method, ctype = r.Method, r.Header.Get("Content-Type")
			raw, _ = io.ReadAll(r.Body)
			writeJSON(w, 201, `{"id":77,"number":12,"status":"draft","link":"https://x/rfis/77"}`)
		},
	})
	body := RFICreate{Subject: "Beam size", Question: RFIQuestion{Body: "What size?"}}
	got, err := newTestClient(f, &sleepRecorder{}).CreateRFI(context.Background(), "tok", 7, 9, body)
	if err != nil {
		t.Fatalf("CreateRFI: %v", err)
	}
	if got != (RFICreated{ID: 77, Number: "12", Status: "draft", Link: "https://x/rfis/77"}) {
		t.Fatalf("created = %+v", got)
	}
	if method != http.MethodPost || ctype != "application/json" {
		t.Errorf("method %q content-type %q", method, ctype)
	}
	r := f.recorded()[0]
	if r.Query["run_configurable_validations"] != "true" || r.Header.Get("Procore-Company-Id") != "7" {
		t.Errorf("query/header = %v %q", r.Query, r.Header.Get("Procore-Company-Id"))
	}
	var top map[string]map[string]json.RawMessage
	if err := json.Unmarshal(raw, &top); err != nil {
		t.Fatalf("body %s: %v", raw, err)
	}
	rfi, ok := top["rfi"]
	if !ok || len(top) != 1 {
		t.Fatalf("body = %s, want only top-level rfi", raw)
	}
	var subject string
	if err := json.Unmarshal(rfi["subject"], &subject); err != nil || subject != "Beam size" {
		t.Errorf("subject = %s", rfi["subject"])
	}
	var q struct {
		Body string `json:"body"`
	}
	if err := json.Unmarshal(rfi["question"], &q); err != nil || q.Body != "What size?" {
		t.Errorf("question = %s", rfi["question"])
	}
	for _, k := range []string{"due_date", "location_id", "cost_code_id", "specification_section_id", "drawing_number", "rfi_manager_id", "responsible_contractor_id", "schedule_impact", "cost_impact", "private", "draft"} {
		if _, present := rfi[k]; present {
			t.Errorf("optional field %q present in %s", k, raw)
		}
	}
}

func TestClient_CreateRFI_RetriesWithFreshBodyOn429(t *testing.T) {
	var bodies []string
	f := newFakeProcore(t, map[string]http.HandlerFunc{
		"/rest/v1.0/projects/9/rfis": func(w http.ResponseWriter, r *http.Request) {
			b, _ := io.ReadAll(r.Body)
			bodies = append(bodies, string(b))
			if len(bodies) == 1 {
				writeJSON(w, http.StatusTooManyRequests, `{}`)
				return
			}
			writeJSON(w, 201, `{"id":1,"number":"A-1"}`)
		},
	})
	_, err := newTestClient(f, &sleepRecorder{}).CreateRFI(context.Background(), "tok", 7, 9, RFICreate{Subject: "S"})
	if err != nil {
		t.Fatalf("CreateRFI: %v", err)
	}
	if len(bodies) != 2 || bodies[0] == "" || bodies[0] != bodies[1] {
		t.Fatalf("bodies = %q", bodies)
	}
}

func TestClient_CreateRFI_422_IsValidationError(t *testing.T) {
	f := newFakeProcore(t, map[string]http.HandlerFunc{
		"/rest/v1.0/projects/9/rfis": func(w http.ResponseWriter, r *http.Request) {
			writeJSON(w, 422, `{"errors":{"due_date":["can't be blank"],"rfi_manager_id":"is required"}}`)
		},
	})
	_, err := newTestClient(f, &sleepRecorder{}).CreateRFI(context.Background(), "tok", 7, 9, RFICreate{Subject: "S"})
	var ve *ValidationError
	if !errors.As(err, &ve) {
		t.Fatalf("err = %v, want *ValidationError", err)
	}
	if got := ve.Fields["due_date"]; len(got) != 1 || got[0] != "can't be blank" {
		t.Errorf("due_date = %v", got)
	}
	if got := ve.Fields["rfi_manager_id"]; len(got) != 1 || got[0] != "is required" {
		t.Errorf("rfi_manager_id = %v", got)
	}
}

// Nested error objects flatten to dotted keys; other leaf shapes keep their
// raw JSON as the message rather than losing the 422's detail.
func TestClient_CreateRFI_NestedErrors_Flattened(t *testing.T) {
	f := newFakeProcore(t, map[string]http.HandlerFunc{
		"/rest/v1.0/projects/9/rfis": func(w http.ResponseWriter, r *http.Request) {
			writeJSON(w, 422, `{"errors":{"question":{"body":["can't be blank"]},"rfi":{"assignees":{"0":"is invalid"}},"subject":"is too long","count":3}}`)
		},
	})
	_, err := newTestClient(f, &sleepRecorder{}).CreateRFI(context.Background(), "tok", 7, 9, RFICreate{Subject: "S"})
	var ve *ValidationError
	if !errors.As(err, &ve) {
		t.Fatalf("err = %v, want *ValidationError", err)
	}
	want := map[string][]string{
		"question.body":   {"can't be blank"},
		"rfi.assignees.0": {"is invalid"},
		"subject":         {"is too long"},
		"count":           {"3"},
	}
	if !reflect.DeepEqual(ve.Fields, want) {
		t.Errorf("fields = %v, want %v", ve.Fields, want)
	}
}

func TestClient_422_UnparsableBody_IsAPIError(t *testing.T) {
	f := newFakeProcore(t, map[string]http.HandlerFunc{
		"/rest/v1.0/projects/9/rfis": func(w http.ResponseWriter, r *http.Request) {
			writeJSON(w, 422, `not json`)
		},
	})
	_, err := newTestClient(f, &sleepRecorder{}).CreateRFI(context.Background(), "tok", 7, 9, RFICreate{Subject: "S"})
	var ve *ValidationError
	var apiErr *APIError
	if errors.As(err, &ve) || !errors.As(err, &apiErr) || apiErr.Status != 422 {
		t.Fatalf("err = %v, want *APIError 422", err)
	}
}

func TestClient_CreateRFI_403_IsErrPermissionDenied(t *testing.T) {
	f := newFakeProcore(t, map[string]http.HandlerFunc{
		"/rest/v1.0/projects/9/rfis": func(w http.ResponseWriter, r *http.Request) {
			writeJSON(w, http.StatusForbidden, `{"errors":"forbidden"}`)
		},
	})
	_, err := newTestClient(f, &sleepRecorder{}).CreateRFI(context.Background(), "tok", 7, 9, RFICreate{Subject: "S"})
	if !errors.Is(err, ErrPermissionDenied) {
		t.Fatalf("err = %v, want ErrPermissionDenied", err)
	}
}
