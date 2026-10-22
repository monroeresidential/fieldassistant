package auth

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/monroeresidential/fieldassistant/api/internal/procore"
)

// newCompanyCacheFixture returns a CompanyCache over a fake Procore REST host
// that lists company 7 to "access-old" and counts /companies requests.
func newCompanyCacheFixture(t *testing.T) (*CompanyCache, *tokenSourceFixture, *atomic.Int32) {
	t.Helper()
	f := newTokenSourceFixture(t)
	f.seed(t, 24*time.Hour)
	var calls atomic.Int32
	rest := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/rest/v1.0/companies" || r.Header.Get("Authorization") != "Bearer access-old" {
			http.Error(w, "unexpected request", http.StatusNotFound)
			return
		}
		calls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode([]procore.Company{{ID: 7, Name: "Monroe Residential"}})
	}))
	t.Cleanup(rest.Close)
	pc := procore.NewClient(rest.URL, rest.Client(), func(time.Duration) {}, f.clock.now)
	return NewCompanyCache(pc, f.ts, 10*time.Minute, f.clock.now), f, &calls
}

func TestCompanyCache_SecondCallDoesNotHitProcore(t *testing.T) {
	c, f, calls := newCompanyCacheFixture(t)
	ctx := context.Background()

	got, err := c.Companies(ctx, f.userID)
	if err != nil {
		t.Fatalf("Companies: %v", err)
	}
	if len(got) != 1 || got[0].ID != 7 {
		t.Fatalf("companies %+v", got)
	}
	visible, err := c.Visible(ctx, f.userID, 7)
	if err != nil || !visible {
		t.Fatalf("Visible(7) = %v, %v; want true", visible, err)
	}
	visible, err = c.Visible(ctx, f.userID, 8)
	if err != nil || visible {
		t.Fatalf("Visible(8) = %v, %v; want false", visible, err)
	}
	if n := calls.Load(); n != 1 {
		t.Fatalf("Procore /companies hit %d times, want 1", n)
	}
}

func TestCompanyCache_RefetchesAfterTTL(t *testing.T) {
	c, f, calls := newCompanyCacheFixture(t)
	ctx := context.Background()

	if _, err := c.Companies(ctx, f.userID); err != nil {
		t.Fatalf("Companies: %v", err)
	}
	f.clock.advance(10*time.Minute + time.Second)
	if _, err := c.Companies(ctx, f.userID); err != nil {
		t.Fatalf("Companies after TTL: %v", err)
	}
	if n := calls.Load(); n != 2 {
		t.Fatalf("Procore /companies hit %d times, want 2", n)
	}
}
