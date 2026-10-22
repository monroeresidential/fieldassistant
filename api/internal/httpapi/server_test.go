package httpapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestProtectedRouteWithoutBearer401(t *testing.T) {
	srv := httptest.NewServer(NewRouter(Deps{}))
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
