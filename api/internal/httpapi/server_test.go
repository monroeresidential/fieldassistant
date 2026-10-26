package httpapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestProtectedRouteWithoutBearer401(t *testing.T) {
	srv := httptest.NewServer(NewRouter(testDeps()))
	defer srv.Close()
	res, err := http.Get(srv.URL + "/v1/companies")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusUnauthorized {
		t.Fatalf("want 401 for missing bearer, got %d", res.StatusCode)
	}
	var e Error
	if err := json.NewDecoder(res.Body).Decode(&e); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if e.Code != "unauthorized" {
		t.Fatalf("code %q", e.Code)
	}
}

func TestMissingRequiredHeaderReturnsJSONBadRequest(t *testing.T) {
	srv, _ := newTestServer(t)
	pair := signIn(t, srv, "device-1")

	res, b := call(t, srv, http.MethodGet, "/v1/projects/1/lookups/users", pair.AccessToken, nil)
	if ct := res.Header.Get("Content-Type"); ct != "application/json" {
		t.Fatalf("content-type %q", ct)
	}
	wantError(t, res, b, http.StatusBadRequest, "bad_request")
}

func TestLogoutWithoutBearer401(t *testing.T) {
	srv := httptest.NewServer(NewRouter(testDeps()))
	defer srv.Close()
	res, err := http.Post(srv.URL+"/v1/auth/logout", "application/json", nil)
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusUnauthorized {
		t.Fatalf("want 401 for missing bearer, got %d", res.StatusCode)
	}
	var e Error
	if err := json.NewDecoder(res.Body).Decode(&e); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if e.Code != "unauthorized" {
		t.Fatalf("code %q", e.Code)
	}
}

func TestPublicAuthRoutesSkipBearerCheck(t *testing.T) {
	srv := httptest.NewServer(NewRouter(testDeps()))
	defer srv.Close()
	// A malformed body is rejected by request decoding, which runs only
	// after the bearer check let the request through.
	res, err := http.Post(srv.URL+"/v1/auth/start", "application/json", strings.NewReader(`{`))
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusBadRequest {
		t.Fatalf("public route /v1/auth/start: want 400 from body decoding, got %d", res.StatusCode)
	}
}
