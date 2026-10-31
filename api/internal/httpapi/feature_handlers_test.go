package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"

	"github.com/monroeresidential/fieldassistant/api/internal/procore"
)

const managersPath = "/rest/v1.0/projects/9/rfis/potential_rfi_managers"

// featureCall sends method path as companyID with an optional JSON body and
// the extra request headers given as name/value pairs.
func featureCall(t *testing.T, srv *httptest.Server, method, path, bearer string, companyID int64, body any, headers ...string) (*http.Response, []byte) {
	t.Helper()
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(context.Background(), method, srv.URL+path, rd)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Authorization", "Bearer "+bearer)
	req.Header.Set("X-Company-Id", strconv.FormatInt(companyID, 10))
	for i := 0; i+1 < len(headers); i += 2 {
		req.Header.Set(headers[i], headers[i+1])
	}
	res, err := noRedirectClient.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	defer res.Body.Close()
	b, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	return res, b
}

// getFeatures sends GET /v1/projects/9/features as companyID.
func getFeatures(t *testing.T, srv *httptest.Server, bearer string, companyID int64, headers ...string) (*http.Response, []byte) {
	t.Helper()
	return featureCall(t, srv, http.MethodGet, "/v1/projects/9/features", bearer, companyID, nil, headers...)
}

// newFeatureServer is newLookupServer with one potential RFI manager on
// project 9.
func newFeatureServer(t *testing.T) (*httptest.Server, *fakeProcore, string) {
	t.Helper()
	srv, fp, token := newLookupServer(t)
	fp.With(func(f *fakeProcore) {
		f.RFIManagers[9] = []procore.RFIManager{{ID: 11, Name: "Ben West", Login: "ben@example.com"}}
	})
	return srv, fp, token
}

// wantRFI asserts b is a FeaturesResponse whose only feature is rfi with
// the given availability and reason ("" for none).
func wantRFI(t *testing.T, b []byte, available bool, reason string) FeaturesResponse {
	t.Helper()
	got := decode[FeaturesResponse](t, b)
	if len(got.Features) != 1 || got.Features[0].Feature != FeatureStateFeatureRfi {
		t.Fatalf("features %+v, want rfi only", got.Features)
	}
	f := got.Features[0]
	gotReason := ""
	if f.Reason != nil {
		gotReason = *f.Reason
	}
	if f.Available != available || gotReason != reason {
		t.Fatalf("rfi available %v reason %q, want %v %q", f.Available, gotReason, available, reason)
	}
	if got.FetchedAt.IsZero() {
		t.Fatal("fetched_at is zero")
	}
	return got
}

func TestGetFeatures_200(t *testing.T) {
	srv, fp, token := newFeatureServer(t)

	res, b := getFeatures(t, srv, token, 7)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status %d; body %s", res.StatusCode, b)
	}
	if got := decode[map[string]any](t, b); got["features"].([]any)[0].(map[string]any)["reason"] != nil {
		t.Fatalf("available feature carries a reason: %s", b)
	}
	wantRFI(t, b, true, "")
	fp.With(func(f *fakeProcore) {
		n := 0
		for _, r := range f.Requests {
			if r.Path == managersPath {
				n++
				if r.Header.Get("Procore-Company-Id") != "7" {
					t.Errorf("managers request Procore-Company-Id %q, want 7", r.Header.Get("Procore-Company-Id"))
				}
			}
		}
		if n != 1 {
			t.Errorf("managers requests = %d, want 1", n)
		}
	})
}

func TestGetFeatures_ReadOnlyUser_UnavailableWithReason(t *testing.T) {
	srv, fp, token := newFeatureServer(t)
	fp.With(func(f *fakeProcore) {
		f.Forbidden["/projects/9/rfis/potential_rfi_managers"] = true
		f.ForbiddenBodies["/projects/9/rfis/potential_rfi_managers"] = `{"errors":["Permission denied"]}`
	})

	res, b := getFeatures(t, srv, token, 7)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status %d; body %s", res.StatusCode, b)
	}
	wantRFI(t, b, false, "Permission denied")
}

func TestGetFeatures_CompanyNotVisible_403(t *testing.T) {
	srv, fp, token := newFeatureServer(t)

	res, b := getFeatures(t, srv, token, 8)
	wantError(t, res, b, http.StatusForbidden, "forbidden_scope")
	if n := fp.requestsTo("/rest/v1.0/projects/9") + fp.requestsTo(managersPath); n != 0 {
		t.Fatalf("Procore received %d project requests for an invisible company", n)
	}
}

func TestGetFeatures_ProjectNotVisible_403(t *testing.T) {
	srv, fp, token := newFeatureServer(t)
	fp.With(func(f *fakeProcore) { delete(f.ProjectDetails, 9) })

	res, b := getFeatures(t, srv, token, 7)
	wantError(t, res, b, http.StatusForbidden, "procore_permission_denied")
	if n := fp.requestsTo(managersPath); n != 0 {
		t.Fatalf("probe ran %d times for an invisible project", n)
	}
}

func TestGetFeatures_ProjectNotVisible_403_Message(t *testing.T) {
	srv, fp, token := newFeatureServer(t)
	fp.With(func(f *fakeProcore) {
		f.Forbidden["/projects/9"] = true
		f.ForbiddenBodies["/projects/9"] = `{"errors":["You do not have access to this project."]}`
	})

	res, b := getFeatures(t, srv, token, 7)
	wantError(t, res, b, http.StatusForbidden, "procore_permission_denied")
	if e := decode[Error](t, b); e.Message != "You do not have access to this project." {
		t.Fatalf("message %q, want Procore's text", e.Message)
	}
}

func TestGetFeatures_NoCache_Refetches(t *testing.T) {
	srv, fp, token := newFeatureServer(t)

	for range 2 {
		if res, b := getFeatures(t, srv, token, 7); res.StatusCode != http.StatusOK {
			t.Fatalf("status %d; body %s", res.StatusCode, b)
		}
	}
	if n := fp.requestsTo(managersPath); n != 1 {
		t.Fatalf("managers requests = %d, want 1 (cached)", n)
	}

	fp.With(func(f *fakeProcore) { f.Forbidden["/projects/9/rfis/potential_rfi_managers"] = true })
	res, b := getFeatures(t, srv, token, 7, "Cache-Control", "no-cache")
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status %d; body %s", res.StatusCode, b)
	}
	if n := fp.requestsTo(managersPath); n != 2 {
		t.Fatalf("managers requests after no-cache = %d, want 2", n)
	}
	wantRFI(t, b, false, "forbidden")
}

func TestGetFeatures_ProcoreDown_502(t *testing.T) {
	srv, fp, token := newFeatureServer(t)
	if res, b := getFeatures(t, srv, token, 7); res.StatusCode != http.StatusOK {
		t.Fatalf("status %d; body %s", res.StatusCode, b)
	}
	fp.Server.Close()

	res, b := getFeatures(t, srv, token, 7, "Cache-Control", "no-cache")
	wantError(t, res, b, http.StatusBadGateway, "procore_unavailable")
}

func TestSyncProject_InvalidatesFeatures(t *testing.T) {
	srv, fp, token := newFeatureServer(t)
	if res, b := getFeatures(t, srv, token, 7); res.StatusCode != http.StatusOK {
		t.Fatalf("status %d; body %s", res.StatusCode, b)
	}

	res, b := featureCall(t, srv, http.MethodPost, "/v1/projects/9/sync", token, 7, SyncRequest{Kinds: []SyncRequestKinds{"rfi"}})
	if res.StatusCode != http.StatusOK {
		t.Fatalf("sync: status %d; body %s", res.StatusCode, b)
	}

	if res, b := getFeatures(t, srv, token, 7); res.StatusCode != http.StatusOK {
		t.Fatalf("status %d; body %s", res.StatusCode, b)
	}
	if n := fp.requestsTo(managersPath); n != 2 {
		t.Fatalf("managers requests = %d, want 2 (sync drops the cached answer)", n)
	}
}
