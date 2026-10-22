package httpapi

import (
	"net/http"
	"net/url"
	"strings"
	"testing"
)

func TestAuthStart_ReturnsAuthorizeURLWithState(t *testing.T) {
	srv, _ := newTestServer(t)

	res, b := call(t, srv, http.MethodPost, "/v1/auth/start", "", AuthStartRequest{DeviceId: "device-1"})
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status %d; body %s", res.StatusCode, b)
	}
	got := decode[AuthStartResponse](t, b)
	if got.State == "" {
		t.Fatal("empty state")
	}
	if !strings.Contains(got.AuthorizeUrl, "state="+url.QueryEscape(got.State)) {
		t.Fatalf("authorize_url %q does not carry state %q", got.AuthorizeUrl, got.State)
	}
	if !strings.Contains(got.AuthorizeUrl, "/oauth/authorize?") {
		t.Fatalf("authorize_url %q is not the Procore authorize endpoint", got.AuthorizeUrl)
	}
}

func TestAuthCallback_FullFlow(t *testing.T) {
	srv, _ := newTestServer(t)

	state := startAuth(t, srv, "device-1")
	code := callbackCode(t, srv, state)

	res, b := call(t, srv, http.MethodPost, "/v1/auth/exchange", "", AuthExchangeRequest{Code: code, DeviceId: "device-1"})
	if res.StatusCode != http.StatusOK {
		t.Fatalf("exchange: status %d; body %s", res.StatusCode, b)
	}
	pair := decode[TokenPair](t, b)
	if pair.AccessToken == "" || pair.RefreshToken == "" || pair.ExpiresIn != 900 {
		t.Fatalf("token pair %+v", pair)
	}

	res, b = call(t, srv, http.MethodGet, "/v1/me", pair.AccessToken, nil)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("me: status %d; body %s", res.StatusCode, b)
	}
	me := decode[User](t, b)
	if me.ProcoreUserId != testProcoreUser.ID {
		t.Fatalf("procore_user_id %d, want %d", me.ProcoreUserId, testProcoreUser.ID)
	}
	if me.Email != testProcoreUser.Login || me.Name != testProcoreUser.Name {
		t.Fatalf("user %+v, want email %q name %q", me, testProcoreUser.Login, testProcoreUser.Name)
	}
	if me.LastCompanyId != nil || me.LastProjectId != nil || me.ReauthRequired {
		t.Fatalf("new user %+v has selection or reauth set", me)
	}
}

func TestAuthCallback_BadState_400(t *testing.T) {
	srv, fp := newTestServer(t)

	q := url.Values{"code": {testProcoreCode}, "state": {"unknown-state"}}
	res, b := call(t, srv, http.MethodGet, "/v1/auth/callback?"+q.Encode(), "", nil)
	wantError(t, res, b, http.StatusBadRequest, "bad_request")
	fp.With(func(f *fakeProcore) {
		if len(f.Requests) != 0 {
			t.Fatalf("bad state still reached Procore: %+v", f.Requests)
		}
	})
}

func TestAuthExchange_WrongDevice_400(t *testing.T) {
	srv, _ := newTestServer(t)

	code := callbackCode(t, srv, startAuth(t, srv, "device-1"))
	res, b := call(t, srv, http.MethodPost, "/v1/auth/exchange", "", AuthExchangeRequest{Code: code, DeviceId: "device-2"})
	wantError(t, res, b, http.StatusBadRequest, "bad_request")
}

func TestAuthExchange_CodeSingleUse(t *testing.T) {
	srv, _ := newTestServer(t)

	code := callbackCode(t, srv, startAuth(t, srv, "device-1"))
	req := AuthExchangeRequest{Code: code, DeviceId: "device-1"}
	if res, b := call(t, srv, http.MethodPost, "/v1/auth/exchange", "", req); res.StatusCode != http.StatusOK {
		t.Fatalf("first exchange: status %d; body %s", res.StatusCode, b)
	}
	res, b := call(t, srv, http.MethodPost, "/v1/auth/exchange", "", req)
	wantError(t, res, b, http.StatusBadRequest, "bad_request")
}

func TestAuthRefresh_RotatesAndRejectsReuse(t *testing.T) {
	srv, _ := newTestServer(t)
	pair := signIn(t, srv, "device-1")

	res, b := call(t, srv, http.MethodPost, "/v1/auth/refresh", "", AuthRefreshRequest{RefreshToken: pair.RefreshToken})
	if res.StatusCode != http.StatusOK {
		t.Fatalf("refresh: status %d; body %s", res.StatusCode, b)
	}
	rotated := decode[TokenPair](t, b)
	if rotated.RefreshToken == "" || rotated.RefreshToken == pair.RefreshToken {
		t.Fatalf("refresh token not rotated: %q", rotated.RefreshToken)
	}

	res, b = call(t, srv, http.MethodPost, "/v1/auth/refresh", "", AuthRefreshRequest{RefreshToken: pair.RefreshToken})
	wantError(t, res, b, http.StatusUnauthorized, "unauthorized")

	// Reuse revokes the whole session, so the rotated pair is dead too.
	res, b = call(t, srv, http.MethodGet, "/v1/me", rotated.AccessToken, nil)
	wantError(t, res, b, http.StatusUnauthorized, "unauthorized")
}

func TestAuthLogout_204_ThenBearerRejected(t *testing.T) {
	srv, fp := newTestServer(t)
	pair := signIn(t, srv, "device-1")

	res, b := call(t, srv, http.MethodPost, "/v1/auth/logout", pair.AccessToken, nil)
	if res.StatusCode != http.StatusNoContent {
		t.Fatalf("logout: status %d; body %s", res.StatusCode, b)
	}

	res, b = call(t, srv, http.MethodGet, "/v1/me", pair.AccessToken, nil)
	wantError(t, res, b, http.StatusUnauthorized, "unauthorized")

	fp.With(func(f *fakeProcore) {
		// The only Procore token pair issued was from the sign-in exchange.
		if len(f.Revoked) != 1 || f.Revoked[0] != "refresh-42-1" {
			t.Fatalf("revoked %q, want the Procore refresh token", f.Revoked)
		}
	})
}

func TestProtectedRoute_NoBearer_401(t *testing.T) {
	srv, _ := newTestServer(t)

	res, b := call(t, srv, http.MethodGet, "/v1/me", "", nil)
	wantError(t, res, b, http.StatusUnauthorized, "unauthorized")

	res, b = call(t, srv, http.MethodGet, "/v1/me", "not-a-jwt", nil)
	wantError(t, res, b, http.StatusUnauthorized, "unauthorized")
}

func TestAuthCallback_ProcoreFailure_502(t *testing.T) {
	srv, _ := newTestServer(t)
	state := startAuth(t, srv, "device-1")

	// The fake rejects authorization codes for users it does not know.
	q := url.Values{"code": {"999"}, "state": {state}}
	res, b := call(t, srv, http.MethodGet, "/v1/auth/callback?"+q.Encode(), "", nil)
	wantError(t, res, b, http.StatusBadGateway, "procore_unavailable")
}
