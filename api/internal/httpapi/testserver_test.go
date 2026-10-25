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
	"github.com/monroeresidential/fieldassistant/api/internal/features/rfi"
	"github.com/monroeresidential/fieldassistant/api/internal/jobs"
	"github.com/monroeresidential/fieldassistant/api/internal/llm/llmtest"
	"github.com/monroeresidential/fieldassistant/api/internal/pipeline"
	"github.com/monroeresidential/fieldassistant/api/internal/procore"
	"github.com/monroeresidential/fieldassistant/api/internal/records"
	"github.com/monroeresidential/fieldassistant/api/internal/store"
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
	Query  url.Values
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
	RefreshErrors bool                        // /oauth/token refresh_token grant answers 500
	Status429Once bool                        // the next REST request answers 429
	Forbidden     map[string]bool             // REST paths without the version prefix, e.g. "/projects/5/rfis", that answer 403

	// Lookup lists by project id. Cost codes and drawings are matched on
	// the project_id query parameter.
	ProjectUsers map[int64][]procore.ProjectUser
	Locations    map[int64][]procore.Location
	SpecSections map[int64][]procore.SpecSection
	CostCodes    map[int64][]procore.CostCode
	Vendors      map[int64][]procore.Vendor
	Drawings     map[int64][]procore.Drawing
	// ProjectDetails answers GET /projects/{id}; a missing project is 404.
	ProjectDetails map[int64]procore.ProjectDetail

	// RFICreateResponse scripts POST /projects/{id}/rfis: it receives the
	// request body and returns the status and JSON body to answer with. It
	// runs under the fake's lock, so it must not call With. Nil answers 500.
	RFICreateResponse func(body []byte) (int, string)
	RFICreateBodies   [][]byte // bodies posted to /projects/{id}/rfis

	// RecordRFIs are raw Show RFI bodies by Procore user id, for the record
	// mirror. For a user with an entry, GET /projects/{id}/rfis lists them
	// (in any project) instead of RFIs, keeping only those whose updated_at
	// falls in filters[updated_at] when it is sent, and
	// GET /projects/{id}/rfis/{rfi} answers with the one whose id matches.
	RecordRFIs map[int64][]map[string]any
	// Files are served without auth at GET /files/{name}; a missing name
	// is 404.
	Files map[string]fakeFile

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
	f.Requests = append(f.Requests, fakeRequest{Method: r.Method, Path: r.URL.Path, Query: r.URL.Query(), Header: r.Header.Clone()})

	switch {
	case r.Method == http.MethodPost && r.URL.Path == "/oauth/token":
		f.token(w, r)
	case r.Method == http.MethodPost && r.URL.Path == "/oauth/revoke":
		f.Revoked = append(f.Revoked, r.FormValue("token"))
		w.WriteHeader(http.StatusOK)
	case strings.HasPrefix(r.URL.Path, "/rest/v1.0/"), strings.HasPrefix(r.URL.Path, "/rest/v1.1/"):
		f.rest(w, r)
	case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/files/"):
		file, ok := f.Files[strings.TrimPrefix(r.URL.Path, "/files/")]
		if !ok {
			http.Error(w, "no such file", http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", file.ContentType)
		_, _ = w.Write(file.Body)
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
		if f.RefreshErrors {
			http.Error(w, "internal error", http.StatusInternalServerError)
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
	version, path := r.URL.Path[len("/rest/"):len("/rest/v1.0")], r.URL.Path[len("/rest/v1.0"):]
	if f.Forbidden[path] {
		http.Error(w, `{"errors":"forbidden"}`, http.StatusForbidden)
		return
	}
	queryProject, _ := strconv.ParseInt(r.URL.Query().Get("project_id"), 10, 64)
	projectID, sub, isProject := projectRoute(path)
	route := r.Method + " " + version + " " + path
	if isProject {
		route = r.Method + " " + version + " /projects/{id}" + sub
		if strings.HasPrefix(sub, "/rfis/") {
			route = r.Method + " " + version + " /projects/{id}/rfis/{rfi}"
		}
	}
	recordRFIs, hasRecordRFIs := f.RecordRFIs[id]
	switch route {
	case "GET v1.0 /me":
		writeJSON(w, me)
	case "GET v1.0 /companies":
		writeJSON(w, pageOf(f.Companies, firstPage))
	case "GET v1.0 /projects":
		companyID, _ := strconv.ParseInt(r.URL.Query().Get("company_id"), 10, 64)
		writeJSON(w, pageOf(f.Projects[companyID], firstPage))
	case "GET v1.0 /projects/{id}":
		detail, ok := f.ProjectDetails[projectID]
		if !ok {
			http.Error(w, `{"errors":"not found"}`, http.StatusNotFound)
			return
		}
		writeJSON(w, detail)
	case "GET v1.0 /projects/{id}/rfis":
		if !hasRecordRFIs {
			writeJSON(w, pageOf(f.RFIs[projectID], firstPage))
			return
		}
		items, ok := filterUpdatedAt(recordRFIs, r.URL.Query().Get("filters[updated_at]"))
		if !ok {
			http.Error(w, `{"errors":"bad filters[updated_at]"}`, http.StatusBadRequest)
			return
		}
		writeJSON(w, pageOf(items, firstPage))
	case "GET v1.0 /projects/{id}/rfis/{rfi}":
		rfiID, _ := strconv.ParseInt(strings.TrimPrefix(sub, "/rfis/"), 10, 64)
		for _, item := range recordRFIs {
			if n, _ := item["id"].(int64); n == rfiID {
				writeJSON(w, item)
				return
			}
		}
		http.Error(w, `{"errors":"not found"}`, http.StatusNotFound)
	case "POST v1.0 /projects/{id}/rfis":
		body, _ := io.ReadAll(r.Body)
		f.RFICreateBodies = append(f.RFICreateBodies, body)
		status, resp := http.StatusInternalServerError, `{"errors":"no scripted response"}`
		if f.RFICreateResponse != nil {
			status, resp = f.RFICreateResponse(body)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = io.WriteString(w, resp)
	case "GET v1.0 /projects/{id}/users":
		writeJSON(w, pageOf(f.ProjectUsers[projectID], firstPage))
	case "GET v1.0 /projects/{id}/locations":
		writeJSON(w, pageOf(f.Locations[projectID], firstPage))
	case "GET v1.0 /projects/{id}/specification_sections":
		writeJSON(w, pageOf(f.SpecSections[projectID], firstPage))
	case "GET v1.0 /projects/{id}/vendors":
		writeJSON(w, pageOf(f.Vendors[projectID], firstPage))
	case "GET v1.0 /cost_codes":
		writeJSON(w, pageOf(f.CostCodes[queryProject], firstPage))
	case "GET v1.1 /drawings":
		writeJSON(w, pageOf(f.Drawings[queryProject], firstPage))
	default:
		http.Error(w, "unexpected request", http.StatusNotFound)
	}
}

// projectRoute splits "/projects/{id}[/sub]" into the id and "/sub". ok is
// false for any other path, including the bare "/projects" list.
func projectRoute(path string) (id int64, sub string, ok bool) {
	rest, found := strings.CutPrefix(path, "/projects/")
	if !found {
		return 0, "", false
	}
	idPart, tail, hasTail := strings.Cut(rest, "/")
	id, err := strconv.ParseInt(idPart, 10, 64)
	if err != nil {
		return 0, "", false
	}
	if hasTail {
		sub = "/" + tail
	}
	return id, sub, true
}

// fakeFile is a file the fake serves at /files/{name}.
type fakeFile struct {
	ContentType string
	Body        []byte
}

// filterUpdatedAt keeps the items whose "updated_at" falls in rng, Procore's
// "<RFC3339 start>...<RFC3339 end>" range; an empty rng keeps every item. ok
// is false when rng is malformed.
func filterUpdatedAt(items []map[string]any, rng string) (kept []map[string]any, ok bool) {
	if rng == "" {
		return items, true
	}
	startS, endS, found := strings.Cut(rng, "...")
	start, err1 := time.Parse(time.RFC3339, startS)
	end, err2 := time.Parse(time.RFC3339, endS)
	if !found || err1 != nil || err2 != nil {
		return nil, false
	}
	kept = []map[string]any{}
	for _, item := range items {
		s, _ := item["updated_at"].(string)
		at, err := time.Parse(time.RFC3339, s)
		if err == nil && !at.Before(start) && !at.After(end) {
			kept = append(kept, item)
		}
	}
	return kept, true
}

// requestsTo returns how many requests the fake received for path.
func (f *fakeProcore) requestsTo(path string) int {
	n := 0
	f.With(func(f *fakeProcore) {
		for _, r := range f.Requests {
			if r.Path == path {
				n++
			}
		}
	})
	return n
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
	return newTestServerWithScheme(t, "fieldassistant")
}

// newTestServerWithScheme is newTestServer with the app redirect scheme set.
func newTestServerWithScheme(t *testing.T, scheme string) (*httptest.Server, *fakeProcore) {
	t.Helper()
	ts := startTestServer(t, scheme)
	return ts.Server, ts.Procore
}

// testServer is a running API test server and its fakes.
type testServer struct {
	Server  *httptest.Server
	Procore *fakeProcore
	// LLM is the extractor behind the drafts pipeline. Set its Steps
	// before creating a draft; every extraction replays them.
	LLM *llmtest.Scripted
	// Store is the test database behind the server.
	Store *store.Store
}

// newDraftTestServer is newTestServer that also returns the scripted
// extractor the drafts pipeline uses.
func newDraftTestServer(t *testing.T) testServer {
	t.Helper()
	return startTestServer(t, "fieldassistant")
}

func startTestServer(t *testing.T, scheme string) testServer {
	t.Helper()
	st := storetest.New(t)
	clock := &fakeClock{Now: time.Now()}

	fp := &fakeProcore{
		Users:     map[int64]procore.Me{testProcoreUser.ID: testProcoreUser},
		Projects:  map[int64][]procore.Project{},
		RFIs:      map[int64][]procore.RFI{},
		Forbidden: map[string]bool{},

		ProjectUsers:   map[int64][]procore.ProjectUser{},
		Locations:      map[int64][]procore.Location{},
		SpecSections:   map[int64][]procore.SpecSection{},
		CostCodes:      map[int64][]procore.CostCode{},
		Vendors:        map[int64][]procore.Vendor{},
		Drawings:       map[int64][]procore.Drawing{},
		ProjectDetails: map[int64]procore.ProjectDetail{},
		RecordRFIs:     map[int64][]map[string]any{},
		Files:          map[string]fakeFile{},

		Clock: clock,
	}
	procoreSrv := httptest.NewServer(fp)
	t.Cleanup(procoreSrv.Close)
	fp.Server = procoreSrv

	// The redirect URL must name the API server, so take its address
	// before building the router.
	api := httptest.NewUnstartedServer(nil)
	apiURL := "http://" + api.Listener.Addr().String()

	cfg := config.Config{
		PublicBaseURL:     apiURL,
		AppRedirectScheme: scheme,
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
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	lookups := pipeline.NewLookups(st, pc, tokens, 10*time.Minute, clock.now, logger)
	scripted := &llmtest.Scripted{Model: "claude-test-model"}
	features := map[string]pipeline.Feature{"rfi": rfi.New()}
	drafts := pipeline.NewDrafts(st, lookups, scripted, pc, tokens, features, "claude-configured", 20*time.Second, clock.now, logger)
	// The River client works submit jobs for the whole test; it stops
	// before the database lock is released.
	riverClient, err := jobs.NewClient(st.Pool, drafts, logger)
	if err != nil {
		t.Fatalf("river client: %v", err)
	}
	if err := riverClient.Start(context.Background()); err != nil {
		t.Fatalf("start river: %v", err)
	}
	t.Cleanup(func() {
		if err := riverClient.Stop(context.Background()); err != nil {
			t.Errorf("stop river: %v", err)
		}
	})

	api.Config.Handler = NewRouter(Deps{
		Logger:    logger,
		Config:    cfg,
		Store:     st,
		Sessions:  sessions,
		Broker:    auth.NewBroker(st, oauth, pc, tokens, sessions, clock.now),
		Tokens:    tokens,
		Procore:   pc,
		Companies: auth.NewCompanyCache(pc, tokens, 10*time.Minute, clock.now),
		Lookups:   lookups,
		Drafts:    drafts,
		Jobs:      riverClient,
		Records: records.NewMirror(st, map[records.Kind]records.Fetcher{
			records.KindRFI: records.RFIFetcher{PC: pc},
		}, tokens, clock.now, logger),
	})
	api.Start()
	t.Cleanup(api.Close)
	return testServer{Server: api, Procore: fp, LLM: scripted, Store: st}
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
	return callbackCodeAs(t, srv, state, testProcoreCode)
}

// callbackCodeAs is callbackCode signing in to Procore with procoreCode, the
// decimal Procore user id.
func callbackCodeAs(t *testing.T, srv *httptest.Server, state, procoreCode string) string {
	t.Helper()
	q := url.Values{"code": {procoreCode}, "state": {state}}
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
	return signInAs(t, srv, deviceID, testProcoreCode)
}

// signInAs is signIn as the Procore user whose decimal id is procoreCode;
// that user must be in the fake's Users.
func signInAs(t *testing.T, srv *httptest.Server, deviceID, procoreCode string) TokenPair {
	t.Helper()
	verifier, challenge := newVerifier(t)
	code := callbackCodeAs(t, srv, startAuth(t, srv, deviceID, challenge), procoreCode)
	res, b := call(t, srv, http.MethodPost, "/v1/auth/exchange", "", AuthExchangeRequest{Code: code, DeviceId: deviceID, CodeVerifier: verifier})
	if res.StatusCode != http.StatusOK {
		t.Fatalf("exchange: status %d; body %s", res.StatusCode, b)
	}
	return decode[TokenPair](t, b)
}
