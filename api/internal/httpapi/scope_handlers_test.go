package httpapi

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/monroeresidential/fieldassistant/api/internal/procore"
)

func TestMe_ReturnsUserAndSelection(t *testing.T) {
	srv, fp := newTestServer(t)
	fp.With(func(f *fakeProcore) { f.Companies = []procore.Company{{ID: 7, Name: "Monroe Residential"}} })
	pair := signIn(t, srv, "device-1")

	res, b := call(t, srv, http.MethodGet, "/v1/me", pair.AccessToken, nil)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("me: status %d; body %s", res.StatusCode, b)
	}
	if me := decode[User](t, b); me.LastCompanyId != nil || me.LastProjectId != nil {
		t.Fatalf("new user has selection %+v", me)
	}

	res, b = call(t, srv, http.MethodPut, "/v1/me/selection", pair.AccessToken, Selection{CompanyId: 7, ProjectId: 99})
	if res.StatusCode != http.StatusNoContent {
		t.Fatalf("put selection: status %d; body %s", res.StatusCode, b)
	}

	res, b = call(t, srv, http.MethodGet, "/v1/me", pair.AccessToken, nil)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("me: status %d; body %s", res.StatusCode, b)
	}
	me := decode[User](t, b)
	if me.LastCompanyId == nil || *me.LastCompanyId != 7 || me.LastProjectId == nil || *me.LastProjectId != 99 {
		t.Fatalf("selection company %v project %v, want 7 and 99", me.LastCompanyId, me.LastProjectId)
	}
}

func TestMeSelection_CompanyNotVisible_403(t *testing.T) {
	srv, fp := newTestServer(t)
	fp.With(func(f *fakeProcore) { f.Companies = []procore.Company{{ID: 7, Name: "Monroe Residential"}} })
	pair := signIn(t, srv, "device-1")

	res, b := call(t, srv, http.MethodPut, "/v1/me/selection", pair.AccessToken, Selection{CompanyId: 8, ProjectId: 99})
	wantError(t, res, b, http.StatusForbidden, "forbidden_scope")

	res, b = call(t, srv, http.MethodGet, "/v1/me", pair.AccessToken, nil)
	if me := decode[User](t, b); me.LastCompanyId != nil {
		t.Fatalf("invisible company saved: %+v", me)
	}
}

func TestCompanies_ListsFromProcore(t *testing.T) {
	srv, fp := newTestServer(t)
	fp.With(func(f *fakeProcore) {
		f.Companies = []procore.Company{{ID: 7, Name: "Monroe Residential"}, {ID: 9, Name: "Monroe Commercial"}}
	})
	pair := signIn(t, srv, "device-1")

	res, b := call(t, srv, http.MethodGet, "/v1/companies", pair.AccessToken, nil)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("companies: status %d; body %s", res.StatusCode, b)
	}
	got := decode[[]Company](t, b)
	want := []Company{{Id: 7, Name: "Monroe Residential"}, {Id: 9, Name: "Monroe Commercial"}}
	if !slices.Equal(got, want) {
		t.Fatalf("companies %+v, want %+v", got, want)
	}
}

func TestCompanies_ProcoreDown_502(t *testing.T) {
	srv, fp := newTestServer(t)
	pair := signIn(t, srv, "device-1")
	fp.Server.Close()

	res, b := call(t, srv, http.MethodGet, "/v1/companies", pair.AccessToken, nil)
	wantError(t, res, b, http.StatusBadGateway, "procore_unavailable")
}

func TestProjects_ListsFromProcore(t *testing.T) {
	srv, fp := newTestServer(t)
	fp.With(func(f *fakeProcore) {
		f.Companies = []procore.Company{{ID: 7, Name: "Monroe Residential"}}
		f.Projects[7] = []procore.Project{{ID: 99, Name: "Elm St", DisplayName: "1001 - Elm St"}}
	})
	pair := signIn(t, srv, "device-1")

	res, b := call(t, srv, http.MethodGet, "/v1/companies/7/projects", pair.AccessToken, nil)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("projects: status %d; body %s", res.StatusCode, b)
	}
	got := decode[[]Project](t, b)
	want := []Project{{Id: 99, Name: "Elm St", DisplayName: "1001 - Elm St"}}
	if !slices.Equal(got, want) {
		t.Fatalf("projects %+v, want %+v", got, want)
	}
}

func TestProjects_CompanyNotVisible_403(t *testing.T) {
	srv, fp := newTestServer(t)
	fp.With(func(f *fakeProcore) {
		f.Companies = []procore.Company{{ID: 7, Name: "Monroe Residential"}}
		f.Projects[8] = []procore.Project{{ID: 1, Name: "Hidden"}}
	})
	pair := signIn(t, srv, "device-1")

	res, b := call(t, srv, http.MethodGet, "/v1/companies/8/projects", pair.AccessToken, nil)
	wantError(t, res, b, http.StatusForbidden, "forbidden_scope")
	fp.With(func(f *fakeProcore) {
		for _, r := range f.Requests {
			if r.Path == "/rest/v1.0/projects" {
				t.Fatalf("Procore received a projects request for an invisible company: %+v", r)
			}
		}
	})
}

// TestRFIs_RequiresCompanyHeader from the brief is covered by
// TestMissingRequiredHeaderReturnsJSONBadRequest in server_test.go.

// getRFIs calls GET /v1/projects/{projectID}/rfis with X-Company-Id
// companyID and returns the response with its body read.
func getRFIs(t *testing.T, srv *httptest.Server, bearer string, companyID, projectID int64) (*http.Response, []byte) {
	t.Helper()
	path := "/v1/projects/" + strconv.FormatInt(projectID, 10) + "/rfis"
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, srv.URL+path, nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Authorization", "Bearer "+bearer)
	req.Header.Set("X-Company-Id", strconv.FormatInt(companyID, 10))
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

func TestRFIs_ListsWithAssigneeNames(t *testing.T) {
	srv, fp := newTestServer(t)
	due := "2026-11-03"
	fp.With(func(f *fakeProcore) {
		f.Companies = []procore.Company{{ID: 7, Name: "Monroe Residential"}}
		f.RFIs[99] = []procore.RFI{
			{ID: 501, Number: "12", Subject: "Window header size", Status: "open", DueDate: &due,
				Assignees: []procore.Person{{ID: 3, Name: "Mike Jones"}}},
			{ID: 502, Number: "13", Subject: "Slab finish", Status: "closed"},
		}
	})
	pair := signIn(t, srv, "device-1")

	res, b := getRFIs(t, srv, pair.AccessToken, 7, 99)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("rfis: status %d; body %s", res.StatusCode, b)
	}
	got := decode[[]RFISummary](t, b)
	if len(got) != 2 {
		t.Fatalf("rfis %+v, want 2", got)
	}
	first := got[0]
	if first.Id != 501 || first.Number != "12" || first.Subject != "Window header size" || first.Status != "open" {
		t.Fatalf("rfi %+v", first)
	}
	if !slices.Equal(first.Assignees, []string{"Mike Jones"}) {
		t.Fatalf("assignees %q, want [Mike Jones]", first.Assignees)
	}
	if first.DueDate == nil || !first.DueDate.Time.Equal(time.Date(2026, 11, 3, 0, 0, 0, 0, time.UTC)) {
		t.Fatalf("due_date %v, want 2026-11-03", first.DueDate)
	}
	if got[1].DueDate != nil || got[1].Assignees == nil || len(got[1].Assignees) != 0 {
		t.Fatalf("rfi without due date or assignees %+v", got[1])
	}
	if !strings.Contains(string(b), `"assignees":[]`) {
		t.Fatalf("empty assignees not encoded as []: %s", b)
	}
	fp.With(func(f *fakeProcore) {
		for _, r := range f.Requests {
			if r.Path == "/rest/v1.0/projects/99/rfis" && r.Header.Get("Procore-Company-Id") != "7" {
				t.Fatalf("RFI request Procore-Company-Id %q, want 7", r.Header.Get("Procore-Company-Id"))
			}
		}
	})
}

func TestRFIs_CompanyNotVisible_403(t *testing.T) {
	srv, fp := newTestServer(t)
	fp.With(func(f *fakeProcore) { f.Companies = []procore.Company{{ID: 7, Name: "Monroe Residential"}} })
	pair := signIn(t, srv, "device-1")

	res, b := getRFIs(t, srv, pair.AccessToken, 8, 99)
	wantError(t, res, b, http.StatusForbidden, "forbidden_scope")
	fp.With(func(f *fakeProcore) {
		for _, r := range f.Requests {
			if strings.HasSuffix(r.Path, "/rfis") {
				t.Fatalf("Procore received an RFI request for an invisible company: %+v", r)
			}
		}
	})
}

func TestRFIs_ProcoreForbidden_MapsToPermissionDenied(t *testing.T) {
	srv, fp := newTestServer(t)
	fp.With(func(f *fakeProcore) {
		f.Companies = []procore.Company{{ID: 7, Name: "Monroe Residential"}}
		f.Forbidden["/projects/99/rfis"] = true
	})
	pair := signIn(t, srv, "device-1")

	res, b := getRFIs(t, srv, pair.AccessToken, 7, 99)
	wantError(t, res, b, http.StatusForbidden, "procore_permission_denied")
	if e := decode[Error](t, b); e.Message != "Procore permission denied for the RFI tool" {
		t.Fatalf("message %q", e.Message)
	}
}

func TestMe_AfterProcoreRevocation_401Reauth(t *testing.T) {
	srv, fp := newTestServer(t)
	fp.With(func(f *fakeProcore) { f.Companies = []procore.Company{{ID: 7, Name: "Monroe Residential"}} })
	pair := signIn(t, srv, "device-1")

	// Warm the company cache so the test also proves it expires with the clock.
	if res, b := call(t, srv, http.MethodGet, "/v1/companies", pair.AccessToken, nil); res.StatusCode != http.StatusOK {
		t.Fatalf("companies: status %d; body %s", res.StatusCode, b)
	}

	// The fake's Procore access tokens last 7200s, so after three hours the
	// stored one is expired and the refresh grant fails as if revoked.
	fp.With(func(f *fakeProcore) {
		f.RefreshFails = true
		f.Clock.Advance(3 * time.Hour)
	})
	// The app access token (15 minutes) expired too; the session has not.
	res, b := call(t, srv, http.MethodPost, "/v1/auth/refresh", "", AuthRefreshRequest{RefreshToken: pair.RefreshToken})
	if res.StatusCode != http.StatusOK {
		t.Fatalf("app refresh: status %d; body %s", res.StatusCode, b)
	}
	bearer := decode[TokenPair](t, b).AccessToken

	res, b = call(t, srv, http.MethodGet, "/v1/companies", bearer, nil)
	wantError(t, res, b, http.StatusUnauthorized, "reauth_required")

	res, b = call(t, srv, http.MethodGet, "/v1/me", bearer, nil)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("me: status %d; body %s", res.StatusCode, b)
	}
	if me := decode[User](t, b); !me.ReauthRequired {
		t.Fatalf("me %+v, want reauth_required true", me)
	}
}
