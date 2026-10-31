package auth

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/monroeresidential/fieldassistant/api/internal/config"
	"github.com/monroeresidential/fieldassistant/api/internal/procore"
	"github.com/monroeresidential/fieldassistant/api/internal/store"
	"github.com/monroeresidential/fieldassistant/api/internal/store/db"
	"github.com/monroeresidential/fieldassistant/api/internal/store/storetest"
)

// fakeRevoke is a Procore login server that records /oauth/revoke calls and
// whether the logging-out session still existed when each arrived.
type fakeRevoke struct {
	srv  *httptest.Server
	fail bool

	mu            sync.Mutex
	revoked       []string
	sessionExists []bool
}

type brokerFixture struct {
	b      *Broker
	st     *store.Store
	login  *fakeRevoke
	sid    uuid.UUID
	userID uuid.UUID
}

func newBrokerFixture(t *testing.T, failRevoke bool) *brokerFixture {
	t.Helper()
	ctx := context.Background()
	st := storetest.New(t)
	clock := &testClock{t: time.Date(2026, 10, 22, 12, 0, 0, 0, time.UTC)}
	u, err := st.Q.UpsertUserByProcoreID(ctx, db.UpsertUserByProcoreIDParams{
		ProcoreUserID: 42, Email: "pm@example.com", Name: "Pat Manager",
	})
	if err != nil {
		t.Fatalf("upsert user: %v", err)
	}
	userID := uuid.UUID(u.ID.Bytes)
	vault, err := NewVault(bytes.Repeat([]byte{7}, 32))
	if err != nil {
		t.Fatalf("NewVault: %v", err)
	}
	ss := NewSessions(st, []byte("test-secret-at-least-32-bytes-long!"), vault, clock.now)
	sid, _, err := ss.Create(ctx, userID, "device-1")
	if err != nil {
		t.Fatalf("Create session: %v", err)
	}

	f := &fakeRevoke{fail: failRevoke}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/oauth/revoke" {
			http.Error(w, "unexpected request", http.StatusNotFound)
			return
		}
		_, err := st.Q.GetSession(r.Context(), pgUUID(sid))
		f.mu.Lock()
		f.revoked = append(f.revoked, r.FormValue("token"))
		f.sessionExists = append(f.sessionExists, err == nil)
		f.mu.Unlock()
		if f.fail {
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(f.srv.Close)

	o := procore.NewOAuth(config.ProcoreConfig{
		ClientID: "cid", ClientSecret: "secret", LoginURL: f.srv.URL,
	}, f.srv.Client(), clock.now)
	ts := NewTokenSource(st, vault, o, clock.now)
	if err := ts.Store(ctx, userID, procore.Tokens{
		AccessToken:  "procore-access",
		RefreshToken: "procore-refresh",
		ExpiresAt:    clock.now().Add(time.Hour),
	}); err != nil {
		t.Fatalf("store Procore tokens: %v", err)
	}
	return &brokerFixture{
		b:      NewBroker(st, o, nil, ts, ss, clock.now),
		st:     st,
		login:  f,
		sid:    sid,
		userID: userID,
	}
}

func TestBroker_Logout_RevokesAtProcoreThenDeletesSession(t *testing.T) {
	f := newBrokerFixture(t, false)
	ctx := context.Background()

	if err := f.b.Logout(ctx, f.sid); err != nil {
		t.Fatalf("Logout: %v", err)
	}
	f.login.mu.Lock()
	revoked, existed := f.login.revoked, f.login.sessionExists
	f.login.mu.Unlock()
	if len(revoked) != 1 || revoked[0] != "procore-refresh" {
		t.Fatalf("revoked %q, want the stored Procore refresh token", revoked)
	}
	if !existed[0] {
		t.Fatal("session was deleted before Procore was asked to revoke")
	}
	if _, err := f.st.Q.GetSession(ctx, pgUUID(f.sid)); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("GetSession after Logout err = %v, want pgx.ErrNoRows", err)
	}
}

func TestBroker_Logout_ProcoreRevokeFails_StillDeletesSession(t *testing.T) {
	f := newBrokerFixture(t, true)
	ctx := context.Background()

	if err := f.b.Logout(ctx, f.sid); err != nil {
		t.Fatalf("Logout with failing Procore revoke: %v", err)
	}
	f.login.mu.Lock()
	n := len(f.login.revoked)
	f.login.mu.Unlock()
	if n != 1 {
		t.Fatalf("revoke calls = %d, want 1", n)
	}
	if _, err := f.st.Q.GetSession(ctx, pgUUID(f.sid)); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("GetSession after Logout err = %v, want pgx.ErrNoRows", err)
	}
}
