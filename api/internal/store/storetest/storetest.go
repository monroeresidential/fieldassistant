// Package storetest provides a migrated, empty Postgres store for tests.
// It is a normal package (not _test.go) so any package's tests can import it.
package storetest

import (
	"context"
	"os"
	"testing"

	"github.com/monroeresidential/fieldassistant/api/internal/store"
)

// LockKey is the session-level advisory lock key that serializes tests
// sharing the test database. `go test ./...` runs package binaries in
// parallel, and each one truncates every table.
const LockKey = 7211042

// New opens the database named by TEST_DATABASE_URL, takes the shared test
// advisory lock for the rest of the test, migrates, and truncates every
// table. The test is skipped when the variable is unset. On cleanup the lock
// is released and the pool closed.
func New(t *testing.T) *store.Store {
	t.Helper()
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("TEST_DATABASE_URL not set; skipping Postgres-backed test")
	}
	ctx := context.Background()

	s, err := store.Open(ctx, url)
	if err != nil {
		t.Fatalf("storetest: open: %v", err)
	}
	// Registered first so it runs last (cleanups are LIFO).
	t.Cleanup(s.Close)

	lockConn, err := s.Pool.Acquire(ctx)
	if err != nil {
		t.Fatalf("storetest: acquire lock conn: %v", err)
	}
	if _, err := lockConn.Exec(ctx, "select pg_advisory_lock($1)", LockKey); err != nil {
		lockConn.Release()
		t.Fatalf("storetest: advisory lock: %v", err)
	}
	t.Cleanup(func() {
		if _, err := lockConn.Exec(context.Background(), "select pg_advisory_unlock($1)", LockKey); err != nil {
			t.Errorf("storetest: advisory unlock: %v", err)
		}
		lockConn.Release()
	})

	if err := s.Migrate(ctx); err != nil {
		t.Fatalf("storetest: migrate: %v", err)
	}
	if _, err := s.Pool.Exec(ctx, "truncate users, sessions, procore_tokens, auth_states, auth_codes, drafts, audit_events, lookup_cache cascade"); err != nil {
		t.Fatalf("storetest: truncate: %v", err)
	}
	return s
}
