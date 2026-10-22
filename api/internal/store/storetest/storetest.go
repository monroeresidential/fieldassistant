// Package storetest provides a migrated, empty Postgres store for tests.
// It is a normal package (not _test.go) so any package's tests can import it.
package storetest

import (
	"context"
	"os"
	"testing"

	"github.com/monroeresidential/fieldassistant/api/internal/store"
)

// New opens the database named by TEST_DATABASE_URL, migrates it, and
// truncates every table. The test is skipped when the variable is unset.
// The pool is closed on test cleanup.
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
	t.Cleanup(s.Close)

	if err := s.Migrate(ctx); err != nil {
		t.Fatalf("storetest: migrate: %v", err)
	}
	if _, err := s.Pool.Exec(ctx, "truncate users, sessions, procore_tokens, auth_states, auth_codes cascade"); err != nil {
		t.Fatalf("storetest: truncate: %v", err)
	}
	return s
}
