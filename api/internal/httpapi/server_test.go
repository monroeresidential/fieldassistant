package httpapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
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
	srv := httptest.NewServer(NewRouter(testDeps()))
	defer srv.Close()
	req, err := http.NewRequest(http.MethodGet, srv.URL+"/v1/projects/1/rfis", nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Authorization", "Bearer x")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("do: %v", err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusBadRequest {
		t.Fatalf("want 400, got %d", res.StatusCode)
	}
	if ct := res.Header.Get("Content-Type"); ct != "application/json" {
		t.Fatalf("content-type %q", ct)
	}
	var e Error
	if err := json.NewDecoder(res.Body).Decode(&e); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if e.Code != "bad_request" {
		t.Fatalf("code %q", e.Code)
	}
}
