package httpapi

import (
	"net/http"
	"slices"
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

func TestCompanies_ProcoreRefresh500_502(t *testing.T) {
	srv, fp := newTestServer(t)
	fp.With(func(f *fakeProcore) { f.Companies = []procore.Company{{ID: 7, Name: "Monroe Residential"}} })
	pair := signIn(t, srv, "device-1")

	// After three hours the stored Procore token is expired, and Procore's
	// token endpoint answers 500 to the refresh grant.
	fp.With(func(f *fakeProcore) {
		f.RefreshErrors = true
		f.Clock.Advance(3 * time.Hour)
	})
	res, b := call(t, srv, http.MethodPost, "/v1/auth/refresh", "", AuthRefreshRequest{RefreshToken: pair.RefreshToken})
	if res.StatusCode != http.StatusOK {
		t.Fatalf("app refresh: status %d; body %s", res.StatusCode, b)
	}
	bearer := decode[TokenPair](t, b).AccessToken

	res, b = call(t, srv, http.MethodGet, "/v1/companies", bearer, nil)
	wantError(t, res, b, http.StatusBadGateway, "procore_unavailable")

	res, b = call(t, srv, http.MethodGet, "/v1/me", bearer, nil)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("me: status %d; body %s", res.StatusCode, b)
	}
	if me := decode[User](t, b); me.ReauthRequired {
		t.Fatalf("me %+v, want reauth_required false after a Procore 500", me)
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

func TestProjects_PermissionDenied_CarriesProcoreReason(t *testing.T) {
	srv, fp := newTestServer(t)
	fp.With(func(f *fakeProcore) {
		f.Companies = []procore.Company{{ID: 7, Name: "Monroe Residential"}}
		f.Forbidden["/projects"] = true
		f.ForbiddenBodies["/projects"] = `{"errors":["App is not connected to this company."]}`
	})
	pair := signIn(t, srv, "device-1")

	res, b := call(t, srv, http.MethodGet, "/v1/companies/7/projects", pair.AccessToken, nil)
	wantError(t, res, b, http.StatusForbidden, "procore_permission_denied")
	if e := decode[Error](t, b); e.Message != "App is not connected to this company." {
		t.Fatalf("message %q, want Procore's text", e.Message)
	}
}

func TestProjects_PermissionDenied_NoBody_GenericMessage(t *testing.T) {
	srv, fp := newTestServer(t)
	fp.With(func(f *fakeProcore) {
		f.Companies = []procore.Company{{ID: 7, Name: "Monroe Residential"}}
		f.Forbidden["/projects"] = true
		f.ForbiddenBodies["/projects"] = ""
	})
	pair := signIn(t, srv, "device-1")

	res, b := call(t, srv, http.MethodGet, "/v1/companies/7/projects", pair.AccessToken, nil)
	wantError(t, res, b, http.StatusForbidden, "procore_permission_denied")
	if e := decode[Error](t, b); e.Message != "Procore permission denied" {
		t.Fatalf("message %q, want the generic text", e.Message)
	}
}
