package auth

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/monroeresidential/fieldassistant/api/internal/config"
	"github.com/monroeresidential/fieldassistant/api/internal/procore"
	"github.com/monroeresidential/fieldassistant/api/internal/store"
	"github.com/monroeresidential/fieldassistant/api/internal/store/db"
	"github.com/monroeresidential/fieldassistant/api/internal/store/storetest"
)

// fakeLogin is an httptest Procore login server that counts refresh grants.
// It accepts only wantRefresh and answers 401 while revoked is set. When
// omitRefresh is set its response carries no refresh_token. While
// serverError is set it answers 500. When gate is
// non-nil each request signals entered and then waits for gate to close.
type fakeLogin struct {
	srv         *httptest.Server
	refreshes   atomic.Int32
	revoked     atomic.Bool
	serverError atomic.Bool
	omitRefresh atomic.Bool
	wantRefresh string
	gate        chan struct{}
	entered     chan struct{}
}

func newFakeLogin(t *testing.T, wantRefresh string) *fakeLogin {
	t.Helper()
	f := &fakeLogin{wantRefresh: wantRefresh}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/oauth/token" || r.FormValue("grant_type") != "refresh_token" {
			http.Error(w, "unexpected request", http.StatusNotFound)
			return
		}
		f.refreshes.Add(1)
		if f.gate != nil {
			f.entered <- struct{}{}
			<-f.gate
		}
		// Widen the window in which concurrent callers overlap.
		time.Sleep(50 * time.Millisecond)
		if f.serverError.Load() {
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		if f.revoked.Load() || r.FormValue("refresh_token") != f.wantRefresh {
			http.Error(w, `{"error":"invalid_grant"}`, http.StatusUnauthorized)
			return
		}
		body := map[string]any{
			"access_token":  "access-new",
			"refresh_token": "refresh-new",
			"expires_in":    7200,
		}
		if f.omitRefresh.Load() {
			delete(body, "refresh_token")
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(body)
	}))
	t.Cleanup(f.srv.Close)
	return f
}

type tokenSourceFixture struct {
	ts     *TokenSource
	st     *store.Store
	vault  *Vault
	login  *fakeLogin
	clock  *testClock
	userID uuid.UUID
}

func newTokenSourceFixture(t *testing.T) *tokenSourceFixture {
	t.Helper()
	st := storetest.New(t)
	clock := &testClock{t: time.Date(2026, 10, 22, 12, 0, 0, 0, time.UTC)}
	u, err := st.Q.UpsertUserByProcoreID(context.Background(), db.UpsertUserByProcoreIDParams{
		ProcoreUserID: 42, Email: "pm@example.com", Name: "Pat Manager",
	})
	if err != nil {
		t.Fatalf("upsert user: %v", err)
	}
	v, err := NewVault(bytes.Repeat([]byte{7}, 32))
	if err != nil {
		t.Fatalf("NewVault: %v", err)
	}
	login := newFakeLogin(t, "refresh-old")
	o := procore.NewOAuth(config.ProcoreConfig{
		ClientID: "cid", ClientSecret: "secret", LoginURL: login.srv.URL,
	}, login.srv.Client(), clock.now)
	return &tokenSourceFixture{
		ts:     NewTokenSource(st, v, o, clock.now),
		st:     st,
		vault:  v,
		login:  login,
		clock:  clock,
		userID: uuid.UUID(u.ID.Bytes),
	}
}

// storedRefresh returns the decrypted refresh token stored for the fixture user.
func (f *tokenSourceFixture) storedRefresh(t *testing.T) string {
	t.Helper()
	row, err := f.st.Q.GetProcoreTokens(context.Background(), pgUUID(f.userID))
	if err != nil {
		t.Fatalf("GetProcoreTokens: %v", err)
	}
	pt, err := f.vault.Decrypt(row.RefreshTokenEnc)
	if err != nil {
		t.Fatalf("decrypt stored refresh token: %v", err)
	}
	return pt
}

// seed stores the "old" token pair expiring ttl from the fixture clock.
func (f *tokenSourceFixture) seed(t *testing.T, ttl time.Duration) {
	t.Helper()
	err := f.ts.Store(context.Background(), f.userID, procore.Tokens{
		AccessToken:  "access-old",
		RefreshToken: "refresh-old",
		ExpiresAt:    f.clock.now().Add(ttl),
	})
	if err != nil {
		t.Fatalf("Store: %v", err)
	}
}

func TestTokenSource_ReturnsStoredWhenFresh(t *testing.T) {
	f := newTokenSourceFixture(t)
	f.seed(t, time.Hour)

	got, err := f.ts.AccessToken(context.Background(), f.userID)
	if err != nil {
		t.Fatalf("AccessToken: %v", err)
	}
	if got != "access-old" {
		t.Fatalf("AccessToken = %q, want %q", got, "access-old")
	}
	if n := f.login.refreshes.Load(); n != 0 {
		t.Fatalf("refresh calls = %d, want 0", n)
	}
}

func TestTokenSource_RefreshesWhenExpiring(t *testing.T) {
	f := newTokenSourceFixture(t)
	f.seed(t, 30*time.Second)
	ctx := context.Background()

	got, err := f.ts.AccessToken(ctx, f.userID)
	if err != nil {
		t.Fatalf("AccessToken: %v", err)
	}
	if got != "access-new" {
		t.Fatalf("AccessToken = %q, want %q", got, "access-new")
	}
	if n := f.login.refreshes.Load(); n != 1 {
		t.Fatalf("refresh calls = %d, want 1", n)
	}

	row, err := f.st.Q.GetProcoreTokens(ctx, pgUUID(f.userID))
	if err != nil {
		t.Fatalf("GetProcoreTokens: %v", err)
	}
	if bytes.Contains(row.AccessTokenEnc, []byte("access-new")) {
		t.Fatal("access token stored in plaintext")
	}
	if bytes.Contains(row.RefreshTokenEnc, []byte("refresh-new")) {
		t.Fatal("refresh token stored in plaintext")
	}
	if pt, err := f.vault.Decrypt(row.AccessTokenEnc); err != nil || pt != "access-new" {
		t.Fatalf("stored access token decrypts to %q, %v; want %q", pt, err, "access-new")
	}
	if pt, err := f.vault.Decrypt(row.RefreshTokenEnc); err != nil || pt != "refresh-new" {
		t.Fatalf("stored refresh token decrypts to %q, %v; want %q", pt, err, "refresh-new")
	}
	wantExp := f.clock.now().Add(7200 * time.Second)
	if !row.ExpiresAt.Time.Equal(wantExp) {
		t.Fatalf("expires_at = %v, want %v", row.ExpiresAt.Time, wantExp)
	}

	// The refreshed token is now fresh: no second refresh.
	if _, err := f.ts.AccessToken(ctx, f.userID); err != nil {
		t.Fatalf("second AccessToken: %v", err)
	}
	if n := f.login.refreshes.Load(); n != 1 {
		t.Fatalf("refresh calls after second AccessToken = %d, want 1", n)
	}
}

func TestTokenSource_ConcurrentCallsRefreshOnce(t *testing.T) {
	f := newTokenSourceFixture(t)
	f.seed(t, 30*time.Second)

	const callers = 20
	var wg sync.WaitGroup
	results := make([]string, callers)
	errs := make([]error, callers)
	for i := range callers {
		wg.Go(func() {
			results[i], errs[i] = f.ts.AccessToken(context.Background(), f.userID)
		})
	}
	wg.Wait()

	for i := range callers {
		if errs[i] != nil {
			t.Fatalf("caller %d: %v", i, errs[i])
		}
		if results[i] != "access-new" {
			t.Fatalf("caller %d got %q, want %q", i, results[i], "access-new")
		}
	}
	if n := f.login.refreshes.Load(); n != 1 {
		t.Fatalf("refresh calls = %d, want 1", n)
	}
}

func TestTokenSource_RefreshRevoked_MarksReauth(t *testing.T) {
	f := newTokenSourceFixture(t)
	f.seed(t, 30*time.Second)
	f.login.revoked.Store(true)
	ctx := context.Background()

	_, err := f.ts.AccessToken(ctx, f.userID)
	if !errors.Is(err, ErrReauthRequired) {
		t.Fatalf("AccessToken err = %v, want ErrReauthRequired", err)
	}
	u, err := f.st.Q.GetUser(ctx, pgUUID(f.userID))
	if err != nil {
		t.Fatalf("GetUser: %v", err)
	}
	if !u.ReauthRequired {
		t.Fatal("users.reauth_required = false, want true")
	}
	// The stored tokens are kept; the next sign-in overwrites them.
	if _, err := f.st.Q.GetProcoreTokens(ctx, pgUUID(f.userID)); err != nil {
		t.Fatalf("GetProcoreTokens after revoke: %v", err)
	}
}

func TestTokenSource_RefreshServerError_IsUnavailable(t *testing.T) {
	f := newTokenSourceFixture(t)
	f.seed(t, 30*time.Second)
	f.login.serverError.Store(true)
	ctx := context.Background()

	_, err := f.ts.AccessToken(ctx, f.userID)
	if !errors.Is(err, procore.ErrUnavailable) {
		t.Fatalf("AccessToken err = %v, want procore.ErrUnavailable", err)
	}
	if errors.Is(err, ErrReauthRequired) {
		t.Fatalf("AccessToken err = %v also wraps ErrReauthRequired", err)
	}
	u, err := f.st.Q.GetUser(ctx, pgUUID(f.userID))
	if err != nil {
		t.Fatalf("GetUser: %v", err)
	}
	if u.ReauthRequired {
		t.Fatal("users.reauth_required = true after a Procore 500, want false")
	}
}

func TestTokenSource_NoStoredTokens_ReauthRequired(t *testing.T) {
	f := newTokenSourceFixture(t)

	_, err := f.ts.AccessToken(context.Background(), f.userID)
	if !errors.Is(err, ErrReauthRequired) {
		t.Fatalf("AccessToken err = %v, want ErrReauthRequired", err)
	}
	if n := f.login.refreshes.Load(); n != 0 {
		t.Fatalf("refresh calls = %d, want 0", n)
	}
}

func TestTokenSource_LeaderCancelDoesNotLoseRefresh(t *testing.T) {
	f := newTokenSourceFixture(t)
	f.seed(t, 30*time.Second)
	f.login.gate = make(chan struct{})
	f.login.entered = make(chan struct{}, 1)

	leaderCtx, cancelLeader := context.WithCancel(context.Background())
	defer cancelLeader()
	var wg sync.WaitGroup
	wg.Go(func() { _, _ = f.ts.AccessToken(leaderCtx, f.userID) })

	select {
	case <-f.login.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("leader never reached the login server")
	}

	var followerTok string
	var followerErr error
	wg.Go(func() { followerTok, followerErr = f.ts.AccessToken(context.Background(), f.userID) })
	time.Sleep(100 * time.Millisecond) // let the follower join the flight

	cancelLeader()
	time.Sleep(50 * time.Millisecond) // let a ctx-bound request observe the cancel
	close(f.login.gate)
	wg.Wait()

	if followerErr != nil {
		t.Fatalf("follower AccessToken: %v", followerErr)
	}
	if followerTok != "access-new" {
		t.Fatalf("follower got %q, want %q", followerTok, "access-new")
	}
	if n := f.login.refreshes.Load(); n != 1 {
		t.Fatalf("refresh calls = %d, want 1", n)
	}
	if got := f.storedRefresh(t); got != "refresh-new" {
		t.Fatalf("stored refresh token = %q, want %q", got, "refresh-new")
	}
}

func TestTokenSource_RefreshWithoutNewRefreshTokenKeepsOld(t *testing.T) {
	f := newTokenSourceFixture(t)
	f.seed(t, 30*time.Second)
	f.login.omitRefresh.Store(true)

	got, err := f.ts.AccessToken(context.Background(), f.userID)
	if err != nil {
		t.Fatalf("AccessToken: %v", err)
	}
	if got != "access-new" {
		t.Fatalf("AccessToken = %q, want %q", got, "access-new")
	}
	if got := f.storedRefresh(t); got != "refresh-old" {
		t.Fatalf("stored refresh token = %q, want %q", got, "refresh-old")
	}
}
