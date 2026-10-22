package storetest_test

import (
	"context"
	"os"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/monroeresidential/fieldassistant/api/internal/store/storetest"
)

func TestStoretest_HoldsAdvisoryLock(t *testing.T) {
	ctx := context.Background()
	storetest.New(t)

	other, err := pgxpool.New(ctx, os.Getenv("TEST_DATABASE_URL"))
	if err != nil {
		t.Fatalf("open second pool: %v", err)
	}
	defer other.Close()

	var n int
	err = other.QueryRow(ctx,
		"select count(*) from pg_locks where locktype = 'advisory' and classid = 0 and objid = $1 and granted",
		storetest.LockKey).Scan(&n)
	if err != nil {
		t.Fatalf("query pg_locks: %v", err)
	}
	if n != 1 {
		t.Fatalf("advisory locks held on key %d = %d, want 1", storetest.LockKey, n)
	}
}
