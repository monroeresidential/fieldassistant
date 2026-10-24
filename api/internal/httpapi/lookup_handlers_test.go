package httpapi

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/monroeresidential/fieldassistant/api/internal/procore"
)

const usersPath = "/rest/v1.0/projects/9/users"

// getLookup sends GET /v1/projects/{projectID}/lookups/{kind} for companyID
// with the extra request headers given as name/value pairs.
func getLookup(t *testing.T, srv *httptest.Server, bearer string, companyID, projectID int64, kind string, headers ...string) (*http.Response, []byte) {
	t.Helper()
	path := "/v1/projects/" + strconv.FormatInt(projectID, 10) + "/lookups/" + kind
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, srv.URL+path, nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Authorization", "Bearer "+bearer)
	req.Header.Set("X-Company-Id", strconv.FormatInt(companyID, 10))
	for i := 0; i+1 < len(headers); i += 2 {
		req.Header.Set(headers[i], headers[i+1])
	}
	res, err := noRedirectClient.Do(req)
	if err != nil {
		t.Fatalf("GET %s: %v", path, err)
	}
	defer res.Body.Close()
	b, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	return res, b
}

// newLookupServer starts a test server where company 7 is visible, project 9
// is readable and has one user, and returns it with a signed-in access token.
func newLookupServer(t *testing.T) (*httptest.Server, *fakeProcore, string) {
	t.Helper()
	srv, fp := newTestServer(t)
	fp.With(func(f *fakeProcore) {
		f.Companies = []procore.Company{{ID: 7, Name: "Monroe Residential"}}
		f.ProjectDetails[9] = procore.ProjectDetail{ID: 9, Name: "Elm Street", DisplayName: "Elm Street", TimeZone: "America/Chicago"}
		f.ProjectUsers[9] = []procore.ProjectUser{{ID: 11, Name: "Ben West", JobTitle: "Architect"}}
	})
	return srv, fp, signIn(t, srv, "device-1").AccessToken
}

func TestGetLookup_ReturnsRowsAndETag(t *testing.T) {
	srv, fp, token := newLookupServer(t)

	res, b := getLookup(t, srv, token, 7, 9, "users")
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status %d; body %s", res.StatusCode, b)
	}
	if res.Header.Get("ETag") == "" {
		t.Fatal("ETag header is empty")
	}
	got := decode[LookupResponse](t, b)
	if got.Kind != "users" || got.Unavailable || got.FetchedAt.IsZero() {
		t.Fatalf("response %+v", got)
	}
	if len(got.Rows) != 1 || got.Rows[0].Id != 11 || got.Rows[0].Label != "Ben West" {
		t.Fatalf("rows %+v, want Ben West", got.Rows)
	}
	if got.Rows[0].Extra == nil || (*got.Rows[0].Extra)["role"] != "Architect" {
		t.Fatalf("extra %v, want role Architect", got.Rows[0].Extra)
	}
	fp.With(func(f *fakeProcore) {
		for _, r := range f.Requests {
			if r.Path == usersPath && r.Header.Get("Procore-Company-Id") != "7" {
				t.Fatalf("users request Procore-Company-Id %q, want 7", r.Header.Get("Procore-Company-Id"))
			}
		}
	})
}

func TestGetLookup_IfNoneMatch_304(t *testing.T) {
	srv, _, token := newLookupServer(t)
	res, b := getLookup(t, srv, token, 7, 9, "users")
	if res.StatusCode != http.StatusOK {
		t.Fatalf("first: status %d; body %s", res.StatusCode, b)
	}
	etag := res.Header.Get("ETag")

	res, b = getLookup(t, srv, token, 7, 9, "users", "If-None-Match", etag)
	if res.StatusCode != http.StatusNotModified {
		t.Fatalf("status %d, want 304; body %s", res.StatusCode, b)
	}
	if len(b) != 0 {
		t.Fatalf("304 body %q, want empty", b)
	}

	res, b = getLookup(t, srv, token, 7, 9, "users", "If-None-Match", `"stale"`)
	if res.StatusCode != http.StatusOK || res.Header.Get("ETag") != etag {
		t.Fatalf("stale If-None-Match: status %d, ETag %q; body %s", res.StatusCode, res.Header.Get("ETag"), b)
	}
}

func TestGetLookup_NoCache_Refetches(t *testing.T) {
	srv, fp, token := newLookupServer(t)
	getLookup(t, srv, token, 7, 9, "users")
	getLookup(t, srv, token, 7, 9, "users")
	if n := fp.requestsTo(usersPath); n != 1 {
		t.Fatalf("users fetched %d times after two cached reads, want 1", n)
	}

	res, b := getLookup(t, srv, token, 7, 9, "users", "Cache-Control", "no-cache")
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status %d; body %s", res.StatusCode, b)
	}
	if n := fp.requestsTo(usersPath); n != 2 {
		t.Fatalf("users fetched %d times after no-cache, want 2", n)
	}
}

func TestGetLookup_BadKind_400(t *testing.T) {
	srv, _, token := newLookupServer(t)
	res, b := getLookup(t, srv, token, 7, 9, "people")
	wantError(t, res, b, http.StatusBadRequest, "bad_request")
}

func TestGetLookup_CompanyNotVisible_403(t *testing.T) {
	srv, fp, token := newLookupServer(t)
	res, b := getLookup(t, srv, token, 8, 9, "users")
	wantError(t, res, b, http.StatusForbidden, "forbidden_scope")
	if n := fp.requestsTo("/rest/v1.0/projects/9") + fp.requestsTo(usersPath); n != 0 {
		t.Fatalf("Procore received %d project requests for an invisible company", n)
	}
}

func TestGetLookup_ProjectNotVisible_403(t *testing.T) {
	srv, _, token := newLookupServer(t)
	res, b := getLookup(t, srv, token, 7, 10, "users")
	wantError(t, res, b, http.StatusForbidden, "procore_permission_denied")
}

func TestGetLookup_Forbidden_Unavailable(t *testing.T) {
	srv, fp, token := newLookupServer(t)
	fp.With(func(f *fakeProcore) { f.Forbidden["/projects/9/users"] = true })

	res, b := getLookup(t, srv, token, 7, 9, "users")
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status %d; body %s", res.StatusCode, b)
	}
	got := decode[LookupResponse](t, b)
	if !got.Unavailable || len(got.Rows) != 0 || got.Reason == nil || *got.Reason == "" {
		t.Fatalf("response %+v, want unavailable with a reason and no rows", got)
	}
	if !strings.Contains(string(b), `"rows":[]`) {
		t.Fatalf("empty rows not encoded as []: %s", b)
	}
	etag := res.Header.Get("ETag")
	if etag == "" {
		t.Fatal("ETag header is empty on an unavailable kind")
	}
	res, b = getLookup(t, srv, token, 7, 9, "users", "If-None-Match", etag)
	if res.StatusCode != http.StatusNotModified {
		t.Fatalf("unavailable with matching If-None-Match: status %d, want 304; body %s", res.StatusCode, b)
	}
}
