// Package pipeline holds the capture, extract, review and submit machinery
// shared by every feature.
package pipeline

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/monroeresidential/fieldassistant/api/internal/auth"
	"github.com/monroeresidential/fieldassistant/api/internal/procore"
	"github.com/monroeresidential/fieldassistant/api/internal/store"
	"github.com/monroeresidential/fieldassistant/api/internal/store/db"
)

// LookupKind names one of a project's lookup lists.
type LookupKind string

const (
	KindUsers        LookupKind = "users"
	KindLocations    LookupKind = "locations"
	KindSpecSections LookupKind = "spec_sections"
	KindCostCodes    LookupKind = "cost_codes"
	KindVendors      LookupKind = "vendors"
	KindDrawings     LookupKind = "drawings"
)

// AllKinds lists every lookup kind in a stable order.
var AllKinds = []LookupKind{KindUsers, KindLocations, KindSpecSections, KindCostCodes, KindVendors, KindDrawings}

// ParseKind returns the kind named s.
func ParseKind(s string) (LookupKind, bool) {
	for _, k := range AllKinds {
		if string(k) == s {
			return k, true
		}
	}
	return "", false
}

// LookupRow is one selectable entry in a lookup list.
type LookupRow struct {
	ID    int64             `json:"id"`
	Label string            `json:"label"`
	Extra map[string]string `json:"extra,omitempty"`
}

// LookupSet is a cached lookup list for one project and kind.
type LookupSet struct {
	Kind        LookupKind
	Rows        []LookupRow
	ETag        string
	Unavailable bool
	Reason      string
	FetchedAt   time.Time
}

// Has reports whether the set contains id.
func (s LookupSet) Has(id int64) bool {
	_, ok := s.Find(id)
	return ok
}

// Find returns the row with id.
func (s LookupSet) Find(id int64) (LookupRow, bool) {
	for _, r := range s.Rows {
		if r.ID == id {
			return r, true
		}
	}
	return LookupRow{}, false
}

// ProcoreLookups is the slice of the Procore client the lookup cache uses.
// *procore.Client satisfies it.
type ProcoreLookups interface {
	ListProjectUsers(ctx context.Context, accessToken string, companyID, projectID int64) ([]procore.ProjectUser, error)
	ListLocations(ctx context.Context, accessToken string, companyID, projectID int64) ([]procore.Location, error)
	ListSpecSections(ctx context.Context, accessToken string, companyID, projectID int64) ([]procore.SpecSection, error)
	ListCostCodes(ctx context.Context, accessToken string, companyID, projectID int64) ([]procore.CostCode, error)
	ListVendors(ctx context.Context, accessToken string, companyID, projectID int64) ([]procore.Vendor, error)
	ListDrawings(ctx context.Context, accessToken string, companyID, projectID int64) ([]procore.Drawing, error)
	GetProject(ctx context.Context, accessToken string, companyID, projectID int64) (procore.ProjectDetail, error)
}

var _ ProcoreLookups = (*procore.Client)(nil)

// AccessTokenSource yields a fresh Procore access token for a user.
// *auth.TokenSource satisfies it.
type AccessTokenSource interface {
	AccessToken(ctx context.Context, userID uuid.UUID) (string, error)
}

type projectKey struct{ company, project int64 }

type accessKey struct {
	user    uuid.UUID
	company int64
	project int64
}

type denyKey struct {
	user    uuid.UUID
	company int64
	project int64
	kind    LookupKind
}

type denial struct {
	reason string
	at     time.Time
}

type projectEntry struct {
	detail procore.ProjectDetail
	at     time.Time
}

// Lookups is a Postgres-backed, TTL-refreshed cache of a project's lookup
// lists, filled from Procore with the requesting user's token.
type Lookups struct {
	st     *store.Store
	pc     ProcoreLookups
	tokens AccessTokenSource
	ttl    time.Duration
	now    func() time.Time
	log    *slog.Logger

	mu       sync.Mutex
	projects map[projectKey]projectEntry
	access   map[accessKey]time.Time // when the user last proved they can see the project
	denied   map[denyKey]denial      // per-user 403s on a lookup kind
}

// NewLookups builds a Lookups. A nil logger means slog.Default().
func NewLookups(st *store.Store, pc ProcoreLookups, tokens AccessTokenSource, ttl time.Duration, now func() time.Time, logger *slog.Logger) *Lookups {
	if logger == nil {
		logger = slog.Default()
	}
	return &Lookups{st: st, pc: pc, tokens: tokens, ttl: ttl, now: now, log: logger, projects: map[projectKey]projectEntry{}, access: map[accessKey]time.Time{}, denied: map[denyKey]denial{}}
}

// Get returns the lookup set for kind. The caller must be able to see the
// project, which is checked with their own token and cached per user for the
// TTL; otherwise procore.ErrPermissionDenied is returned. A stored row younger
// than the TTL is served unless force is set. Otherwise the list is fetched
// from Procore and stored. A 403 on the list is remembered for that user only
// and returned as an unavailable set; the shared row is left alone. Token
// errors surface. Any other failure serves the stale row when there is one and
// returns the error when there is not.
func (l *Lookups) Get(ctx context.Context, userID uuid.UUID, companyID, projectID int64, kind LookupKind, force bool) (LookupSet, error) {
	if err := l.requireAccess(ctx, userID, companyID, projectID); err != nil {
		return LookupSet{}, err
	}
	if !force {
		if d, ok := l.denial(userID, companyID, projectID, kind); ok {
			return LookupSet{Kind: kind, Rows: []LookupRow{}, Unavailable: true, Reason: d.reason, FetchedAt: d.at}, nil
		}
	}

	stale, haveStale, err := l.load(ctx, companyID, projectID, kind)
	if err != nil {
		return LookupSet{}, err
	}
	if haveStale && !force && l.now().Sub(stale.FetchedAt) < l.ttl {
		return stale, nil
	}

	rows, fetchErr := l.fetch(ctx, userID, companyID, projectID, kind)
	switch {
	case fetchErr == nil:
		l.clearDenial(userID, companyID, projectID, kind)
		return l.save(ctx, companyID, projectID, kind, rows, false, "")
	case errors.Is(fetchErr, procore.ErrPermissionDenied):
		d := denial{reason: denialReason(fetchErr), at: l.now()}
		l.mu.Lock()
		l.denied[denyKey{userID, companyID, projectID, kind}] = d
		l.mu.Unlock()
		return LookupSet{Kind: kind, Rows: []LookupRow{}, Unavailable: true, Reason: d.reason, FetchedAt: d.at}, nil
	case errors.Is(fetchErr, auth.ErrReauthRequired), errors.Is(fetchErr, procore.ErrUnauthorized):
		return LookupSet{}, fetchErr
	case haveStale:
		l.log.Warn("lookup refresh failed", "kind", kind, "company_id", companyID, "project_id", projectID, "error", fetchErr)
		return stale, nil
	default:
		return LookupSet{}, fetchErr
	}
}

func (l *Lookups) denial(userID uuid.UUID, companyID, projectID int64, kind LookupKind) (denial, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	d, ok := l.denied[denyKey{userID, companyID, projectID, kind}]
	if !ok || l.now().Sub(d.at) >= l.ttl {
		return denial{}, false
	}
	return d, true
}

func (l *Lookups) clearDenial(userID uuid.UUID, companyID, projectID int64, kind LookupKind) {
	l.mu.Lock()
	delete(l.denied, denyKey{userID, companyID, projectID, kind})
	l.mu.Unlock()
}

// requireAccess confirms the user can see the project, using the cached
// answer when it is younger than the TTL.
func (l *Lookups) requireAccess(ctx context.Context, userID uuid.UUID, companyID, projectID int64) error {
	l.mu.Lock()
	at, ok := l.access[accessKey{userID, companyID, projectID}]
	l.mu.Unlock()
	if ok && l.now().Sub(at) < l.ttl {
		return nil
	}
	_, err := l.fetchProject(ctx, userID, companyID, projectID)
	return err
}

// fetchProject reads the project with the user's own token. Success records
// the user's access and refreshes the shared detail cache. A 403 or 404 means
// the user cannot see the project and is reported as ErrPermissionDenied.
func (l *Lookups) fetchProject(ctx context.Context, userID uuid.UUID, companyID, projectID int64) (procore.ProjectDetail, error) {
	token, err := l.tokens.AccessToken(ctx, userID)
	if err != nil {
		return procore.ProjectDetail{}, err
	}
	detail, err := l.pc.GetProject(ctx, token, companyID, projectID)
	if err != nil {
		var api *procore.APIError
		if errors.Is(err, procore.ErrPermissionDenied) || (errors.As(err, &api) && api.Status == http.StatusNotFound) {
			return procore.ProjectDetail{}, procore.ErrPermissionDenied
		}
		return procore.ProjectDetail{}, err
	}
	now := l.now()
	l.mu.Lock()
	l.access[accessKey{userID, companyID, projectID}] = now
	l.projects[projectKey{companyID, projectID}] = projectEntry{detail: detail, at: now}
	l.mu.Unlock()
	return detail, nil
}

// Warm calls Get for each kind. A failure on one kind does not stop the
// others. It returns the sets that succeeded and the first error.
func (l *Lookups) Warm(ctx context.Context, userID uuid.UUID, companyID, projectID int64, kinds []LookupKind) (map[LookupKind]LookupSet, error) {
	out := make(map[LookupKind]LookupSet, len(kinds))
	var first error
	for _, k := range kinds {
		set, err := l.Get(ctx, userID, companyID, projectID, k, false)
		if err != nil {
			if first == nil {
				first = err
			}
			continue
		}
		out[k] = set
	}
	return out, first
}

// Project returns the project detail. The caller must be able to see the
// project (procore.ErrPermissionDenied otherwise). One Procore call per user
// per project per TTL serves both the access check and the detail.
func (l *Lookups) Project(ctx context.Context, userID uuid.UUID, companyID, projectID int64) (procore.ProjectDetail, error) {
	l.mu.Lock()
	at, seen := l.access[accessKey{userID, companyID, projectID}]
	e, cached := l.projects[projectKey{companyID, projectID}]
	l.mu.Unlock()
	now := l.now()
	if seen && now.Sub(at) < l.ttl && cached && now.Sub(e.at) < l.ttl {
		return e.detail, nil
	}
	return l.fetchProject(ctx, userID, companyID, projectID)
}

// load reads the stored set. The bool is false when no row exists.
func (l *Lookups) load(ctx context.Context, companyID, projectID int64, kind LookupKind) (LookupSet, bool, error) {
	row, err := l.st.Q.GetLookup(ctx, db.GetLookupParams{CompanyID: companyID, ProjectID: projectID, Kind: string(kind)})
	if errors.Is(err, pgx.ErrNoRows) {
		return LookupSet{}, false, nil
	}
	if err != nil {
		return LookupSet{}, false, fmt.Errorf("pipeline: load lookup %s: %w", kind, err)
	}
	rows := []LookupRow{}
	if err := json.Unmarshal(row.Body, &rows); err != nil {
		return LookupSet{}, false, fmt.Errorf("pipeline: decode lookup %s: %w", kind, err)
	}
	return LookupSet{
		Kind:        kind,
		Rows:        rows,
		ETag:        row.Etag,
		Unavailable: row.Unavailable,
		Reason:      row.Reason.String,
		FetchedAt:   row.FetchedAt.Time,
	}, true, nil
}

func (l *Lookups) save(ctx context.Context, companyID, projectID int64, kind LookupKind, rows []LookupRow, unavailable bool, reason string) (LookupSet, error) {
	if rows == nil {
		rows = []LookupRow{}
	}
	body, err := json.Marshal(rows)
	if err != nil {
		return LookupSet{}, fmt.Errorf("pipeline: encode lookup %s: %w", kind, err)
	}
	sum := sha256.Sum256(body)
	etag := `"` + hex.EncodeToString(sum[:])[:16] + `"`
	fetchedAt := l.now()
	err = l.st.Q.UpsertLookup(ctx, db.UpsertLookupParams{
		CompanyID:   companyID,
		ProjectID:   projectID,
		Kind:        string(kind),
		Etag:        etag,
		Body:        body,
		Unavailable: unavailable,
		Reason:      pgtype.Text{String: reason, Valid: unavailable},
		FetchedAt:   pgtype.Timestamptz{Time: fetchedAt, Valid: true},
	})
	if err != nil {
		return LookupSet{}, fmt.Errorf("pipeline: store lookup %s: %w", kind, err)
	}
	return LookupSet{Kind: kind, Rows: rows, ETag: etag, Unavailable: unavailable, Reason: reason, FetchedAt: fetchedAt}, nil
}

func (l *Lookups) fetch(ctx context.Context, userID uuid.UUID, companyID, projectID int64, kind LookupKind) ([]LookupRow, error) {
	token, err := l.tokens.AccessToken(ctx, userID)
	if err != nil {
		return nil, err
	}
	var items any
	switch kind {
	case KindUsers:
		items, err = l.pc.ListProjectUsers(ctx, token, companyID, projectID)
	case KindLocations:
		items, err = l.pc.ListLocations(ctx, token, companyID, projectID)
	case KindSpecSections:
		items, err = l.pc.ListSpecSections(ctx, token, companyID, projectID)
	case KindCostCodes:
		items, err = l.pc.ListCostCodes(ctx, token, companyID, projectID)
	case KindVendors:
		items, err = l.pc.ListVendors(ctx, token, companyID, projectID)
	case KindDrawings:
		items, err = l.pc.ListDrawings(ctx, token, companyID, projectID)
	default:
		return nil, fmt.Errorf("pipeline: unknown lookup kind %q", kind)
	}
	if err != nil {
		return nil, err
	}
	return rowsFor(kind, items), nil
}

// denialReason is the Procore message behind a 403, falling back to the full
// error text.
func denialReason(err error) string {
	var api *procore.APIError
	if errors.As(err, &api) && api.Body != "" {
		return api.Body
	}
	return err.Error()
}
