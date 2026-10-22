package auth

import (
	"context"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/monroeresidential/fieldassistant/api/internal/procore"
)

// CompanyCache remembers each user's Procore company list for a TTL, so
// scope checks on every request do not each cost a Procore call. Failed
// lookups are not cached.
type CompanyCache struct {
	pc  *procore.Client
	ts  *TokenSource
	ttl time.Duration
	now func() time.Time

	mu      sync.Mutex
	entries map[uuid.UUID]companyEntry
}

type companyEntry struct {
	companies []procore.Company
	expires   time.Time
}

// NewCompanyCache returns a CompanyCache listing companies through pc with
// tokens from ts, keeping each list for ttl as measured by now.
func NewCompanyCache(pc *procore.Client, ts *TokenSource, ttl time.Duration, now func() time.Time) *CompanyCache {
	return &CompanyCache{pc: pc, ts: ts, ttl: ttl, now: now, entries: map[uuid.UUID]companyEntry{}}
}

// Companies returns the companies userID can access in Procore, from the
// cache when the entry is younger than the TTL. Errors from the token source
// (such as ErrReauthRequired) and from Procore are returned as they are.
func (c *CompanyCache) Companies(ctx context.Context, userID uuid.UUID) ([]procore.Company, error) {
	c.mu.Lock()
	e, ok := c.entries[userID]
	c.mu.Unlock()
	if ok && c.now().Before(e.expires) {
		return e.companies, nil
	}

	token, err := c.ts.AccessToken(ctx, userID)
	if err != nil {
		return nil, err
	}
	companies, err := c.pc.ListCompanies(ctx, token)
	if err != nil {
		return nil, err
	}
	c.mu.Lock()
	c.entries[userID] = companyEntry{companies: companies, expires: c.now().Add(c.ttl)}
	c.mu.Unlock()
	return companies, nil
}

// Visible reports whether companyID is among the companies userID can access.
func (c *CompanyCache) Visible(ctx context.Context, userID uuid.UUID, companyID int64) (bool, error) {
	companies, err := c.Companies(ctx, userID)
	if err != nil {
		return false, err
	}
	for _, co := range companies {
		if co.ID == companyID {
			return true, nil
		}
	}
	return false, nil
}
