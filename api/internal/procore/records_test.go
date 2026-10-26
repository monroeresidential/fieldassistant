package procore

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"
)

const rfisPath = "/rest/v1.0/projects/376187/rfis"

// rawRFIs returns a JSON array of n RFI items with ids starting at first.
func rawRFIs(first, n int) string {
	parts := make([]string, n)
	for i := range n {
		parts[i] = fmt.Sprintf(`{"id":%d,"subject":"S%d","updated_at":"2026-10-21T12:00:%02dZ"}`, first+i, first+i, i%60)
	}
	return "[" + strings.Join(parts, ",") + "]"
}

func TestListRFIsRaw_SendsUpdatedAtRangeAndPaginates(t *testing.T) {
	f := newFakeProcore(t, map[string]http.HandlerFunc{
		rfisPath: func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Query().Get("page") == "1" {
				writeJSON(w, 200, rawRFIs(1, 100))
				return
			}
			writeJSON(w, 200, rawRFIs(101, 50))
		},
	})
	c := NewClient(f.srv.URL, f.srv.Client(), nil, nil)
	since := time.Date(2026, 10, 20, 10, 0, 0, 0, time.UTC)
	got, err := c.ListRFIsRaw(context.Background(), "tok", 4292442, 376187, &since)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 150 {
		t.Fatalf("got %d records, want 150", len(got))
	}
	if got[0].ID != 1 || got[149].ID != 150 {
		t.Errorf("ids = %d..%d", got[0].ID, got[149].ID)
	}
	if want := time.Date(2026, 10, 21, 12, 0, 0, 0, time.UTC); !got[0].UpdatedAt.Equal(want) {
		t.Errorf("UpdatedAt = %v, want %v", got[0].UpdatedAt, want)
	}
	if want := `{"id":1,"subject":"S1","updated_at":"2026-10-21T12:00:00Z"}`; string(got[0].Body) != want {
		t.Errorf("Body = %s, want %s", got[0].Body, want)
	}
	reqs := f.recorded()
	if len(reqs) != 2 {
		t.Fatalf("requests = %d, want 2", len(reqs))
	}
	for _, rq := range reqs {
		if rq.Query["per_page"] != "100" {
			t.Errorf("per_page = %q", rq.Query["per_page"])
		}
		if rq.Header.Get("Procore-Company-Id") != "4292442" {
			t.Errorf("company header = %q", rq.Header.Get("Procore-Company-Id"))
		}
		start, end, ok := strings.Cut(rq.Query["filters[updated_at]"], "...")
		if !ok || start != "2026-10-20T10:00:00Z" {
			t.Fatalf("filters[updated_at] = %q", rq.Query["filters[updated_at]"])
		}
		end2, err := time.Parse(time.RFC3339, end)
		if err != nil {
			t.Fatalf("range end %q: %v", end, err)
		}
		if d := time.Since(end2); d < -5*time.Second || d > 5*time.Second {
			t.Errorf("range end %v is not within 5s of now", end2)
		}
	}
}

func TestListRFIsRaw_NilSinceOmitsFilter(t *testing.T) {
	f := newFakeProcore(t, map[string]http.HandlerFunc{
		rfisPath: func(w http.ResponseWriter, r *http.Request) { writeJSON(w, 200, rawRFIs(1, 2)) },
	})
	c := NewClient(f.srv.URL, f.srv.Client(), nil, nil)
	got, err := c.ListRFIsRaw(context.Background(), "tok", 4292442, 376187, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d records", len(got))
	}
	if _, ok := f.recorded()[0].Query["filters[updated_at]"]; ok {
		t.Error("filters[updated_at] sent without updatedSince")
	}
}

func TestGetRFIRaw_ReturnsBody(t *testing.T) {
	body := `{"id":182885,"subject":"X","questions":[]}`
	f := newFakeProcore(t, map[string]http.HandlerFunc{
		rfisPath + "/182885": func(w http.ResponseWriter, r *http.Request) { writeJSON(w, 200, body) },
	})
	c := NewClient(f.srv.URL, f.srv.Client(), nil, nil)
	got, err := c.GetRFIRaw(context.Background(), "tok", 4292442, 376187, 182885)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != body {
		t.Errorf("body = %s", got)
	}
	if h := f.recorded()[0].Header.Get("Procore-Company-Id"); h != "4292442" {
		t.Errorf("company header = %q", h)
	}
}

func TestRFIDetail_DecodesDocsSample(t *testing.T) {
	b, err := os.ReadFile("testdata/rfi_show.json")
	if err != nil {
		t.Fatal(err)
	}
	var d RFIDetail
	if err := json.Unmarshal(b, &d); err != nil {
		t.Fatal(err)
	}
	if d.Subject != "Level 2 slab penetration at gridline C4" || d.Number != "12" {
		t.Errorf("subject/number = %q / %q", d.Subject, d.Number)
	}
	if d.SpecificationSection == nil || d.SpecificationSection.Label != "03 30 00 - Cast-in-Place Concrete" {
		t.Errorf("specification_section = %+v", d.SpecificationSection)
	}
	if d.CostCode == nil || d.CostCode.FullCode != "03-300" {
		t.Errorf("cost_code = %+v", d.CostCode)
	}
	if d.CostImpact == nil || d.CostImpact.Status != "yes_known" || d.CostImpact.Value != "1500.00" {
		t.Errorf("cost_impact = %+v", d.CostImpact)
	}
	if d.ReceivedFrom != nil || d.TimeResolved != nil {
		t.Errorf("null fields should decode to nil: %v %v", d.ReceivedFrom, d.TimeResolved)
	}
	if len(d.Questions) != 1 {
		t.Fatalf("questions = %d", len(d.Questions))
	}
	q := d.Questions[0]
	// Procore gives a question's created_by as a bare name string.
	if q.CreatedBy == nil || q.CreatedBy.Name != "Drew Monroe" || q.CreatedBy.ID != 0 {
		t.Errorf("question created_by = %+v, want name only", q.CreatedBy)
	}
	if a := q.Answers[0].CreatedBy; a == nil || a.ID != 302 || a.Name != "Ana Torres" {
		t.Errorf("answer created_by = %+v", a)
	}
	if len(q.Attachments) != 1 || q.Attachments[0].ID != 9001 {
		t.Errorf("question attachments = %+v", q.Attachments)
	}
	if len(q.Answers) != 1 || q.Answers[0].PlainTextBody != "Coring is acceptable if it clears the rebar by 2 in." {
		t.Errorf("answers = %+v", q.Answers)
	}
	if len(q.Answers[0].Attachments) != 1 || q.Answers[0].Attachments[0].Filename != "sketch.pdf" {
		t.Errorf("answer attachments = %+v", q.Answers[0].Attachments)
	}
	if len(d.DistributionList) != 2 || d.DrawingIDs[1] != 4002 || d.CustomFields["custom_field_1"] != "North stair" {
		t.Errorf("lists/custom fields: %+v %v %v", d.DistributionList, d.DrawingIDs, d.CustomFields)
	}
}

func TestRFIDetail_NumberAsString(t *testing.T) {
	var d RFIDetail
	if err := json.Unmarshal([]byte(`{"id":1,"number":"7.1"}`), &d); err != nil {
		t.Fatal(err)
	}
	if d.Number != "7.1" {
		t.Errorf("number = %q", d.Number)
	}
}

func TestFetchURL_RedirectsOnlyToHTTPS(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/to-https":
			http.Redirect(w, r, "https://"+r.Host+"/file", http.StatusFound)
		case "/to-http":
			http.Redirect(w, r, "http://"+r.Host+"/file", http.StatusFound)
		case "/file":
			_, _ = w.Write([]byte("bytes"))
		}
	}))
	defer srv.Close()
	c := NewClient("http://unused.invalid", srv.Client(), nil, nil)

	resp, err := c.FetchURL(context.Background(), srv.URL+"/to-https")
	if err != nil {
		t.Fatalf("https redirect: %v", err)
	}
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if string(b) != "bytes" {
		t.Fatalf("https redirect body %q", b)
	}

	resp, err = c.FetchURL(context.Background(), srv.URL+"/to-http")
	if err == nil {
		resp.Body.Close()
		t.Fatal("redirect to http was followed")
	}
	if !errors.Is(err, ErrUnavailable) {
		t.Fatalf("redirect to http error %v, want ErrUnavailable", err)
	}
}

func TestFetchURL_MalformedURLIsUnavailable(t *testing.T) {
	c := NewClient("http://unused.invalid", http.DefaultClient, nil, nil)
	resp, err := c.FetchURL(context.Background(), "://no scheme")
	if err == nil {
		resp.Body.Close()
		t.Fatal("malformed URL was fetched")
	}
	if !errors.Is(err, ErrUnavailable) {
		t.Fatalf("error %v, want ErrUnavailable", err)
	}
}

func TestFetchURL_NoProcoreHeaders(t *testing.T) {
	var gotAuth, gotCompany string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth, gotCompany = r.Header.Get("Authorization"), r.Header.Get("Procore-Company-Id")
		_, _ = w.Write([]byte("bytes"))
	}))
	defer srv.Close()
	c := NewClient("http://unused.invalid", srv.Client(), nil, nil)
	resp, err := c.FetchURL(context.Background(), srv.URL+"/file?sig=1")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	if string(b) != "bytes" || gotAuth != "" || gotCompany != "" {
		t.Errorf("body=%q auth=%q company=%q", b, gotAuth, gotCompany)
	}
}
