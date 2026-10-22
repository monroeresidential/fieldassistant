package procore

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/monroeresidential/fieldassistant/api/internal/config"
)

var fixedNow = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

func newTestOAuth(loginURL string, hc *http.Client) *OAuth {
	return NewOAuth(config.ProcoreConfig{
		ClientID:     "cid",
		ClientSecret: "s3cret",
		LoginURL:     loginURL,
		RedirectURL:  "https://app.example.com/cb",
	}, hc, func() time.Time { return fixedNow })
}

func TestAuthorizeURL(t *testing.T) {
	o := newTestOAuth("https://login.example.com", nil)
	raw := o.AuthorizeURL("st&te=1")
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if u.Scheme != "https" || u.Host != "login.example.com" || u.Path != "/oauth/authorize" {
		t.Errorf("unexpected base: %s", raw)
	}
	q := u.Query()
	want := map[string]string{
		"response_type": "code",
		"client_id":     "cid",
		"redirect_uri":  "https://app.example.com/cb",
		"state":         "st&te=1",
	}
	for k, v := range want {
		if got := q.Get(k); got != v {
			t.Errorf("%s = %q, want %q", k, got, v)
		}
	}
}

func TestExchange_PostsFormAndParses(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/oauth/token" {
			t.Errorf("got %s %s", r.Method, r.URL.Path)
		}
		if ct := r.Header.Get("Content-Type"); ct != "application/x-www-form-urlencoded" {
			t.Errorf("content-type = %q", ct)
		}
		if err := r.ParseForm(); err != nil {
			t.Errorf("parse form: %v", err)
		}
		want := map[string]string{
			"grant_type":    "authorization_code",
			"code":          "abc",
			"client_id":     "cid",
			"client_secret": "s3cret",
			"redirect_uri":  "https://app.example.com/cb",
		}
		for k, v := range want {
			if got := r.PostForm.Get(k); got != v {
				t.Errorf("form %s = %q, want %q", k, got, v)
			}
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"access_token":"at","token_type":"Bearer","expires_in":7200,"refresh_token":"rt","created_at":1700000000}`))
	}))
	defer srv.Close()

	tok, err := newTestOAuth(srv.URL, srv.Client()).Exchange(context.Background(), "abc")
	if err != nil {
		t.Fatalf("Exchange: %v", err)
	}
	if tok.AccessToken != "at" || tok.RefreshToken != "rt" {
		t.Errorf("tokens = %+v", tok)
	}
	if want := time.Unix(1700000000+7200, 0); !tok.ExpiresAt.Equal(want) {
		t.Errorf("ExpiresAt = %v, want %v", tok.ExpiresAt, want)
	}
}

func TestExchange_MissingCreatedAtUsesClock(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"access_token":"at","expires_in":60,"refresh_token":"rt"}`))
	}))
	defer srv.Close()

	tok, err := newTestOAuth(srv.URL, srv.Client()).Exchange(context.Background(), "abc")
	if err != nil {
		t.Fatalf("Exchange: %v", err)
	}
	if want := fixedNow.Add(60 * time.Second); !tok.ExpiresAt.Equal(want) {
		t.Errorf("ExpiresAt = %v, want %v", tok.ExpiresAt, want)
	}
}

func TestExchange_ErrorOmitsSecretAndTruncatesBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(strings.Repeat("x", 1000) + "s3cret"))
	}))
	defer srv.Close()

	_, err := newTestOAuth(srv.URL, srv.Client()).Exchange(context.Background(), "abc")
	if err == nil {
		t.Fatal("expected error")
	}
	msg := err.Error()
	if !strings.Contains(msg, "500") {
		t.Errorf("error lacks status: %v", msg)
	}
	if strings.Contains(msg, "s3cret") || len(msg) > 400 {
		t.Errorf("error leaks or is too long (%d bytes)", len(msg))
	}
}

func TestRefresh_PostsFormAndParses(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		want := map[string]string{
			"grant_type":    "refresh_token",
			"refresh_token": "old",
			"client_id":     "cid",
			"client_secret": "s3cret",
		}
		for k, v := range want {
			if got := r.PostForm.Get(k); got != v {
				t.Errorf("form %s = %q, want %q", k, got, v)
			}
		}
		_, _ = w.Write([]byte(`{"access_token":"at2","expires_in":7200,"refresh_token":"rt2","created_at":1700000000}`))
	}))
	defer srv.Close()

	tok, err := newTestOAuth(srv.URL, srv.Client()).Refresh(context.Background(), "old")
	if err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	if tok.AccessToken != "at2" || tok.RefreshToken != "rt2" {
		t.Errorf("tokens = %+v", tok)
	}
}

func TestRefresh_RevokedReturnsErrTokenRevoked(t *testing.T) {
	for _, status := range []int{http.StatusUnauthorized, http.StatusBadRequest} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(status)
			_, _ = w.Write([]byte(`{"error":"invalid_grant"}`))
		}))
		_, err := newTestOAuth(srv.URL, srv.Client()).Refresh(context.Background(), "old")
		srv.Close()
		if !errors.Is(err, ErrTokenRevoked) {
			t.Errorf("status %d: err = %v, want ErrTokenRevoked", status, err)
		}
	}
}

func TestRevoke_PostsToken(t *testing.T) {
	var gotToken string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/oauth/revoke" {
			t.Errorf("got %s %s", r.Method, r.URL.Path)
		}
		_ = r.ParseForm()
		gotToken = r.PostForm.Get("token")
		if r.PostForm.Get("client_id") != "cid" || r.PostForm.Get("client_secret") != "s3cret" {
			t.Errorf("missing client credentials: %v", r.PostForm)
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	if err := newTestOAuth(srv.URL, srv.Client()).Revoke(context.Background(), "tok"); err != nil {
		t.Fatalf("Revoke: %v", err)
	}
	if gotToken != "tok" {
		t.Errorf("token = %q", gotToken)
	}
}

func TestRevoke_NonSuccessIsError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()
	if err := newTestOAuth(srv.URL, srv.Client()).Revoke(context.Background(), "tok"); err == nil {
		t.Fatal("expected error")
	}
}
