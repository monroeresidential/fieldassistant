package httpapi

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/monroeresidential/fieldassistant/api/internal/auth"
	"github.com/monroeresidential/fieldassistant/api/internal/config"
	"github.com/monroeresidential/fieldassistant/api/internal/procore"
	"github.com/monroeresidential/fieldassistant/api/internal/store/storetest"
)

// fakeClock is the injected time source for every dependency in a test
// server. It starts at the real current time so the database clock, which
// the consume queries use, agrees with it.
type fakeClock struct {
	mu  sync.Mutex
	Now time.Time
}

func (c *fakeClock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.Now
}

// Advance moves the clock forward by d.
func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.Now = c.Now.Add(d)
}

// fakeRequest is one request the fake Procore server received.
type fakeRequest struct {
	Method string
	Path   string
	Header http.Header
}

// fakeProcore is a fake Procore login and REST host on one httptest server.
//
// An authorization code is the decimal Procore user id to sign in as, e.g.
// "42". Tokens it issues embed that id ("access-42-1", "refresh-42-1"), and
// GET /rest/v1.0/me answers with Users[id]. Mutate the fields through With
// once the server is serving requests.
type fakeProcore struct {
	mu sync.Mutex

	Users         map[int64]procore.Me
	Companies     []procore.Company
	Projects      map[int64][]procore.Project // by company id
	RFIs          map[int64][]procore.RFI     // by project id
	RefreshFails  bool                        // /oauth/token refresh_token grant answers 401
	Status429Once bool                        // the next REST request answers 429
	Forbidden     map[string]bool             // REST paths, e.g. "/projects/5/rfis", that answer 403

	Revoked  []string      // tokens posted to /oauth/revoke
	Requests []fakeRequest // every request, in arrival order

	// Clock is the test server's injected clock.
	Clock *fakeClock
	// Server is the httptest server serving the fake; close it to make
	// Procore unreachable.
	Server *httptest.Server

	issued int
}

// With runs fn while holding the fake's lock, for reading or changing it.
func (f *fakeProcore) With(fn func(f *fakeProcore)) {
	f.mu.Lock()
	defer f.mu.Unlock()
	fn(f)
}

func (f *fakeProcore) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.Requests = append(f.Requests, fakeRequest{Method: r.Method, Path: r.URL.Path, Header: r.Header.Clone()})

	switch {
	case r.Method == http.MethodPost && r.URL.Path == "/oauth/token":
		f.token(w, r)
	case r.Method == http.MethodPost && r.URL.Path == "/oauth/revoke":
		f.Revoked = append(f.Revoked, r.FormValue("token"))
		w.WriteHeader(http.StatusOK)
	case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/rest/v1.0/"):
		f.rest(w, r)
	default:
		http.Error(w, "unexpected request", http.StatusNotFound)
	}
}

func (f *fakeProcore) token(w http.ResponseWriter, r *http.Request) {
	var userID string
	switch r.FormValue("grant_type") {
	case "authorization_code":
		userID = r.FormValue("code")
	case "refresh_token":
		if f.RefreshFails {
			http.Error(w, `{"error":"invalid_grant"}`, http.StatusUnauthorized)
			return
		}
		userID = tokenUserID(r.FormValue("refresh_token"))
	}
	id, err := strconv.ParseInt(userID, 10, 64)
	if _, ok := f.Users[id]; err != nil || !ok {
		http.Error(w, `{"error":"invalid_grant"}`, http.StatusBadRequest)
		return
	}
	f.issued++
	n := strconv.Itoa(f.issued)
	writeJSON(w, map[string]any{
		"access_token":  "access-" + userID + "-" + n,
		"refresh_token": "refresh-" + userID + "-" + n,
		"expires_in":    7200,
	})
}

func (f *fakeProcore) rest(w http.ResponseWriter, r *http.Request) {
	if f.Status429Once {
		f.Status429Once = false
		w.Header().Set("X-Rate-Limit-Reset", strconv.FormatInt(f.Clock.now().Unix(), 10))
		http.Error(w, "rate limited", http.StatusTooManyRequests)
		return
	}
	bearer, _ := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	id, err := strconv.ParseInt(tokenUserID(bearer), 10, 64)
	me, ok := f.Users[id]
	if err != nil || !ok || !strings.HasPrefix(bearer, "access-") {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	// Every list fits on the first page, so later pages are empty.
	page, _ := strconv.Atoi(r.URL.Query().Get("page"))
	firstPage := page <= 1
	path := strings.TrimPrefix(r.URL.Path, "/rest/v1.0")
	if f.Forbidden[path] {
		http.Error(w, `{"errors":"forbidden"}`, http.StatusForbidden)
		return
	}
	switch {
	case path == "/me":
		writeJSON(w, me)
	case path == "/companies":
		writeJSON(w, pageOf(f.Companies, firstPage))
	case path == "/projects":
		companyID, _ := strconv.ParseInt(r.URL.Query().Get("company_id"), 10, 64)
		writeJSON(w, pageOf(f.Projects[companyID], firstPage))
	case strings.HasPrefix(path, "/projects/") && strings.HasSuffix(path, "/rfis"):
		projectID, _ := strconv.ParseInt(strings.TrimSuffix(strings.TrimPrefix(path, "/projects/"), "/rfis"), 10, 64)
		writeJSON(w, pageOf(f.RFIs[projectID], firstPage))
	default:
		http.Error(w, "unexpected request", http.StatusNotFound)
	}
}

// tokenUserID extracts the user id from a token shaped "<kind>-<id>-<n>".
func tokenUserID(tok string) string {
	parts := strings.Split(tok, "-")
	if len(parts) != 3 {
		return ""
	}
	return parts[1]
}

func pageOf[T any](items []T, firstPage bool) []T {
	if !firstPage || items == nil {
		return []T{}
	}
	return items
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

// testProcoreUser is the Procore user newTestServer seeds; sign in as them
// with authorization code testProcoreCode.
var testProcoreUser = procore.Me{ID: 42, Login: "pm@example.com", Name: "Pat Manager"}

const testProcoreCode = "42"

// newTestServer starts a fake Procore host and the API wired to it with real
// dependencies over a fresh test database. It skips without TEST_DATABASE_URL.
func newTestServer(t *testing.T) (*httptest.Server, *fakeProcore) {
	t.Helper()
	st := storetest.New(t)
	clock := &fakeClock{Now: time.Now()}

	fp := &fakeProcore{
		Users:     map[int64]procore.Me{testProcoreUser.ID: testProcoreUser},
		Projects:  map[int64][]procore.Project{},
		RFIs:      map[int64][]procore.RFI{},
		Forbidden: map[string]bool{},
		Clock:     clock,
	}
	procoreSrv := httptest.NewServer(fp)
	t.Cleanup(procoreSrv.Close)
	fp.Server = procoreSrv

	// The redirect URL must name the API server, so take its address
	// before building the router.
	api := httptest.NewUnstartedServer(nil)
	apiURL := "http://" + api.Listener.Addr().String()

	cfg := config.Config{
		PublicBaseURL: apiURL,
		Procore: config.ProcoreConfig{
			ClientID:     "client-id",
			ClientSecret: "client-secret",
			LoginURL:     procoreSrv.URL,
			APIURL:       procoreSrv.URL,
			RedirectURL:  apiURL + "/v1/auth/callback",
		},
		TokenVaultKey: bytes.Repeat([]byte{7}, 32),
		SessionSecret: []byte("test-session-secret-at-least-32-bytes"),
	}
	vault, err := auth.NewVault(cfg.TokenVaultKey)
	if err != nil {
		t.Fatalf("vault: %v", err)
	}
	oauth := procore.NewOAuth(cfg.Procore, procoreSrv.Client(), clock.now)
	pc := procore.NewClient(cfg.Procore.APIURL, procoreSrv.Client(), func(time.Duration) {}, clock.now)
	tokens := auth.NewTokenSource(st, vault, oauth, clock.now)
	sessions := auth.NewSessions(st, cfg.SessionSecret, clock.now)

	api.Config.Handler = NewRouter(Deps{
		Logger:    slog.New(slog.NewTextHandler(io.Discard, nil)),
		Config:    cfg,
		Store:     st,
		Sessions:  sessions,
		Broker:    auth.NewBroker(st, oauth, pc, tokens, sessions, clock.now),
		Tokens:    tokens,
		Procore:   pc,
		Companies: auth.NewCompanyCache(pc, tokens, 10*time.Minute, clock.now),
	})
	api.Start()
	t.Cleanup(api.Close)
	return api, fp
}

// noRedirectClient returns redirects to the caller instead of following them.
var noRedirectClient = &http.Client{
	CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
}

// call sends method path with an optional JSON body and bearer token and
// returns the response with its body read.
func call(t *testing.T, srv *httptest.Server, method, path, bearer string, body any) (*http.Response, []byte) {
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
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
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

// decode unmarshals b into a T, failing the test on error.
func decode[T any](t *testing.T, b []byte) T {
	t.Helper()
	var v T
	if err := json.Unmarshal(b, &v); err != nil {
		t.Fatalf("decode %s: %v", b, err)
	}
	return v
}

// wantError asserts res is an Error JSON response with status and code.
func wantError(t *testing.T, res *http.Response, b []byte, status int, code string) {
	t.Helper()
	if res.StatusCode != status {
		t.Fatalf("status %d, want %d; body %s", res.StatusCode, status, b)
	}
	if e := decode[Error](t, b); e.Code != code {
		t.Fatalf("code %q, want %q", e.Code, code)
	}
}

// newVerifier returns a fresh PKCE-style code verifier (32 random bytes,
// base64url) and its challenge, base64url-nopad(SHA-256(verifier)).
func newVerifier(t *testing.T) (verifier, challenge string) {
	t.Helper()
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		t.Fatalf("random verifier: %v", err)
	}
	verifier = base64.RawURLEncoding.EncodeToString(b)
	sum := sha256.Sum256([]byte(verifier))
	return verifier, base64.RawURLEncoding.EncodeToString(sum[:])
}

// startAuth runs POST /v1/auth/start for deviceID and challenge and returns
// its state.
func startAuth(t *testing.T, srv *httptest.Server, deviceID, challenge string) string {
	t.Helper()
	res, b := call(t, srv, http.MethodPost, "/v1/auth/start", "", AuthStartRequest{DeviceId: deviceID, CodeChallenge: challenge})
	if res.StatusCode != http.StatusOK {
		t.Fatalf("start: status %d; body %s", res.StatusCode, b)
	}
	return decode[AuthStartResponse](t, b).State
}

// callbackCode completes the Procore callback for state and returns the
// one-time code from the app redirect.
func callbackCode(t *testing.T, srv *httptest.Server, state string) string {
	t.Helper()
	q := url.Values{"code": {testProcoreCode}, "state": {state}}
	res, b := call(t, srv, http.MethodGet, "/v1/auth/callback?"+q.Encode(), "", nil)
	if res.StatusCode != http.StatusFound {
		t.Fatalf("callback: status %d; body %s", res.StatusCode, b)
	}
	loc, err := url.Parse(res.Header.Get("Location"))
	if err != nil {
		t.Fatalf("parse Location: %v", err)
	}
	if loc.Scheme != "fieldassistant" || loc.Host != "auth" {
		t.Fatalf("Location %q, want fieldassistant://auth?code=…", loc)
	}
	code := loc.Query().Get("code")
	if code == "" {
		t.Fatalf("Location %q has no code", loc)
	}
	return code
}

// signIn runs start, callback and exchange for deviceID and returns the pair.
func signIn(t *testing.T, srv *httptest.Server, deviceID string) TokenPair {
	t.Helper()
	verifier, challenge := newVerifier(t)
	code := callbackCode(t, srv, startAuth(t, srv, deviceID, challenge))
	res, b := call(t, srv, http.MethodPost, "/v1/auth/exchange", "", AuthExchangeRequest{Code: code, DeviceId: deviceID, CodeVerifier: verifier})
	if res.StatusCode != http.StatusOK {
		t.Fatalf("exchange: status %d; body %s", res.StatusCode, b)
	}
	return decode[TokenPair](t, b)
}
