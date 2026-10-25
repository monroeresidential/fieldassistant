// Package records mirrors each user's view of a project's Procore records
// (RFIs first) in Postgres, so the phone can read them through a delta feed
// and keep them offline. Every row belongs to one user: a record is stored
// only because that user's own token could list it.
package records

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
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

// Kind names a mirrored Procore record type.
type Kind string

// allKinds lists every mirrored kind.
var allKinds = []Kind{KindRFI}

// ParseKind returns the kind named s.
func ParseKind(s string) (Kind, bool) {
	for _, k := range allKinds {
		if string(k) == s {
			return k, true
		}
	}
	return "", false
}

// Fetcher lists one kind of record in a project with a user's token: every
// record when since is nil, otherwise those updated since then.
type Fetcher interface {
	ListSince(ctx context.Context, token string, companyID, projectID int64, since *time.Time) ([]procore.RawRecord, error)
}

// TokenSource yields a fresh Procore access token for a user.
// *auth.TokenSource satisfies it. It is declared here, identical to
// pipeline.AccessTokenSource, so records does not depend on pipeline.
type TokenSource interface {
	AccessToken(ctx context.Context, userID uuid.UUID) (string, error)
}

var _ TokenSource = (*auth.TokenSource)(nil)

const (
	// FreshnessWindow is how long a pull satisfies later unforced syncs.
	FreshnessWindow = 60 * time.Second
	// Overlap is subtracted from the high-water mark for delta pulls, to
	// cover Procore clock skew and records saved during the last pull.
	Overlap = 5 * time.Minute
	// SweepInterval is how often a full pull runs to find deleted records.
	SweepInterval = 6 * time.Hour
	// WarmWindow is how long after a user last asked for a pair WarmAll
	// keeps it pulled.
	WarmWindow = 24 * time.Hour
	// MaxPageLimit caps a Delta page.
	MaxPageLimit = 500
)

const (
	// warmTimeout bounds each pair's sync in WarmAll.
	warmTimeout = 30 * time.Second
	// firstPullWarmTimeout bounds a WarmAll sync of a pair that has never
	// pulled successfully (a first pull, or the first after a permission
	// loss reset it). A large project's first pull may not fit in the
	// handler's 30 s; the warm job then finishes it in the background.
	firstPullWarmTimeout = 2 * time.Minute
	// maxFirstPullsPerTick caps the first pulls one WarmAll run takes on,
	// so a run stays inside the warm job's timeout; the rest wait a tick.
	maxFirstPullsPerTick = 2
	// sweepBackoff is how long automatic sweeps are skipped for a key after
	// one fails, so deltas keep the mirror current when a full pull cannot
	// finish in its budget.
	sweepBackoff = 15 * time.Minute
)

// Pull outcomes reported in PullStatus.Status.
const (
	StatusFresh  = "fresh"
	StatusPulled = "pulled"
	StatusFailed = "failed"
)

// Error codes reported in PullStatus.Code on a failed pull.
const (
	CodePermissionDenied = "procore_permission_denied"
	CodeUnavailable      = "procore_unavailable"
	CodeReauthRequired   = "reauth_required"
	CodeInternal         = "internal"
)

// SyncOpts tunes a Sync. Force ignores FreshnessWindow; Full lists every
// record and soft-deletes the ones no longer visible.
type SyncOpts struct{ Force, Full bool }

// PullStatus reports what a Sync did. A failed pull is a status, not an
// error: the mirror still serves what it has. PulledAt is the last
// successful pull, if any.
type PullStatus struct {
	Kind     Kind
	Status   string // StatusFresh, StatusPulled or StatusFailed
	PulledAt *time.Time
	Code     string // on failed: one of the Code constants
	Message  string // on failed: a short sentence for the user
}

// Record is one mirrored record as the delta feed returns it.
type Record struct {
	ID        int64
	Kind      Kind
	UpdatedAt time.Time
	ChangeSeq int64
	Body      json.RawMessage
	Deleted   bool
}

// DeltaPage is one page of a user's changes since a cursor.
type DeltaPage struct {
	Records    []Record // live rows only (Deleted is false)
	DeletedIDs []int64
	NextSince  int64
	HasMore    bool
	SyncedAt   *time.Time // the last successful pull, if any
}

// Mirror keeps the per-user record mirror in step with Procore.
type Mirror struct {
	st       *store.Store
	fetchers map[Kind]Fetcher
	tokens   TokenSource
	now      func() time.Time
	log      *slog.Logger

	// sweepDeferredUntil maps a sync key to the end of its sweep backoff.
	// It is in memory by design: a restart simply retries the sweep.
	mu                 sync.Mutex
	sweepDeferredUntil map[string]time.Time
}

// NewMirror builds a Mirror. A nil logger means slog.Default().
func NewMirror(st *store.Store, fetchers map[Kind]Fetcher, tokens TokenSource, now func() time.Time, logger *slog.Logger) *Mirror {
	if logger == nil {
		logger = slog.Default()
	}
	return &Mirror{st: st, fetchers: fetchers, tokens: tokens, now: now, log: logger, sweepDeferredUntil: map[string]time.Time{}}
}

// sweepDeferred reports whether key's automatic sweep is backed off at now.
func (m *Mirror) sweepDeferred(key string, now time.Time) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	until, ok := m.sweepDeferredUntil[key]
	if ok && !now.Before(until) {
		delete(m.sweepDeferredUntil, key)
		return false
	}
	return ok
}

// deferSweep backs off key's automatic sweep until *until; a nil until
// clears the backoff.
func (m *Mirror) deferSweep(key string, until *time.Time) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if until == nil {
		delete(m.sweepDeferredUntil, key)
		return
	}
	m.sweepDeferredUntil[key] = *until
}

// Sync pulls kind for the user's project from Procore into the mirror.
//
// The access token is read first, outside the transaction: the token source
// reads and refreshes through the same pool, and asking for a second
// connection while the transaction holds one can exhaust a small pool. The
// pull then runs in one transaction holding an advisory lock on (user,
// project, kind), so a concurrent Sync for the same key waits and then
// finds the pull fresh. last_requested_at is always updated. Within
// FreshnessWindow of the last pull, and without opts.Force, nothing is
// fetched (a token failure does not matter then). A pull is full (every
// record, then a sweep that soft-deletes the rest) when opts.Full is set,
// there is no high-water mark, or a sweep is due (none has run or the last
// is older than SweepInterval) and not backed off; otherwise it lists
// records updated since the high-water mark minus Overlap. A full pull that
// fails on a key with a high-water mark backs automatic sweeps off for
// sweepBackoff, so deltas keep running. A token or fetch error is recorded
// in last_error and reported as a failed status. A permission denial (a
// Procore 403, or a 404 on the list, which means the project or its RFI
// tool is no longer visible to the user) soft-deletes every live row for
// the key, so clients are told the records are gone and nothing the user
// can no longer see is served, and clears the cursors so the next good
// pull is full. Any other error changes no record rows and leaves the
// cursors as they were.
func (m *Mirror) Sync(ctx context.Context, userID uuid.UUID, companyID, projectID int64, kind Kind, opts SyncOpts) PullStatus {
	return m.sync(ctx, userID, companyID, projectID, kind, opts, true)
}

// sync is Sync; touch says whether this call counts as the user asking, which
// moves last_requested_at. WarmAll's pulls do not, so pairs age out of
// WarmWindow.
func (m *Mirror) sync(ctx context.Context, userID uuid.UUID, companyID, projectID int64, kind Kind, opts SyncOpts, touch bool) PullStatus {
	fetcher, ok := m.fetchers[kind]
	if !ok {
		return PullStatus{Kind: kind, Status: StatusFailed, Code: CodeInternal, Message: "This record type cannot be synced."}
	}

	// Read before the transaction; see Sync. A token a few seconds older
	// than the lock is fine.
	token, tokenErr := m.tokens.AccessToken(ctx, userID)

	var (
		out          PullStatus
		fetchErr     error
		full         bool // the pull attempted was full
		fetched      bool // the fetcher was called
		hadHighWater bool // the key had a high-water mark before the pull
	)
	uid := pgtype.UUID{Bytes: userID, Valid: true}
	lockKey := fmt.Sprintf("%s|%d|%s", userID, projectID, kind)
	txErr := m.st.WithPgxTx(ctx, func(tx pgx.Tx, q *db.Queries) error {
		if _, err := tx.Exec(ctx, "select pg_advisory_xact_lock(hashtext($1))", lockKey); err != nil {
			return fmt.Errorf("records: lock: %w", err)
		}
		now := m.now()

		state, err := q.GetSyncState(ctx, db.GetSyncStateParams{UserID: uid, ProjectID: projectID, Kind: string(kind)})
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return fmt.Errorf("records: get sync state: %w", err)
		}
		state.CompanyID, state.UserID, state.ProjectID, state.Kind = companyID, uid, projectID, string(kind)
		if touch || !state.LastRequestedAt.Valid {
			state.LastRequestedAt = ts(now)
		}

		if !opts.Force && state.LastPullAt.Valid && now.Sub(state.LastPullAt.Time) < FreshnessWindow {
			out = PullStatus{Kind: kind, Status: StatusFresh, PulledAt: timePtr(state.LastPullAt)}
			return saveState(ctx, q, state)
		}

		sweepDue := !state.LastSweepAt.Valid || now.Sub(state.LastSweepAt.Time) > SweepInterval
		hadHighWater = state.HighWater.Valid
		full = opts.Full || !hadHighWater || (sweepDue && !m.sweepDeferred(lockKey, now))
		var since *time.Time
		if !full {
			s := state.HighWater.Time.Add(-Overlap)
			since = &s
		}

		var recs []procore.RawRecord
		err = tokenErr
		if err == nil {
			fetched = true
			recs, err = fetcher.ListSince(ctx, token, companyID, projectID, since)
		}
		if err != nil {
			fetchErr = err
			code, msg := classify(err)
			if code == CodePermissionDenied {
				// The user lost access: soft-delete their copy, so clients
				// receive every id in deleted_ids, and start over with a
				// full pull once access returns. The empty, non-nil keep
				// list sweeps every live row.
				if err := q.SoftDeleteMissingRecords(ctx, db.SoftDeleteMissingRecordsParams{
					DeletedAt: ts(now), UserID: uid, ProjectID: projectID, Kind: string(kind), KeepIDs: []int64{},
				}); err != nil {
					return fmt.Errorf("records: purge after permission loss: %w", err)
				}
				state = db.UserSyncState{
					CompanyID: companyID, UserID: uid, ProjectID: projectID, Kind: string(kind),
					LastRequestedAt: state.LastRequestedAt,
				}
			}
			state.LastError = pgtype.Text{String: code + ": " + err.Error(), Valid: true}
			out = PullStatus{Kind: kind, Status: StatusFailed, PulledAt: timePtr(state.LastPullAt), Code: code, Message: msg}
			return saveState(ctx, q, state)
		}

		keep := make([]int64, 0, len(recs)) // non-nil: an empty full fetch sweeps everything
		highWater := state.HighWater
		for _, r := range recs {
			if _, err := q.UpsertRecord(ctx, db.UpsertRecordParams{
				CompanyID: companyID, UserID: uid, ProjectID: projectID, Kind: string(kind),
				ProcoreID: r.ID, ProcoreUpdatedAt: ts(r.UpdatedAt),
				Body: r.Body, BodyHash: bodyHash(r.Body),
			}); err != nil {
				return fmt.Errorf("records: upsert %s %d: %w", kind, r.ID, err)
			}
			keep = append(keep, r.ID)
			if !highWater.Valid || r.UpdatedAt.After(highWater.Time) {
				highWater = ts(r.UpdatedAt)
			}
		}
		if full {
			if err := q.SoftDeleteMissingRecords(ctx, db.SoftDeleteMissingRecordsParams{
				DeletedAt: ts(now), UserID: uid, ProjectID: projectID, Kind: string(kind), KeepIDs: keep,
			}); err != nil {
				return fmt.Errorf("records: sweep: %w", err)
			}
			state.LastSweepAt = ts(now)
		}
		state.HighWater = highWater
		state.LastPullAt = ts(now)
		state.LastError = pgtype.Text{}
		out = PullStatus{Kind: kind, Status: StatusPulled, PulledAt: timePtr(state.LastPullAt)}
		return saveState(ctx, q, state)
	})
	if txErr != nil {
		err := txErr
		if fetchErr != nil {
			// Recording the fetch failure failed too (often the same
			// cancelled context); report the fetch error.
			err = fetchErr
		}
		m.log.Error("records sync failed", "kind", kind, "company_id", companyID, "project_id", projectID, "error", txErr)
		code, msg := classify(err)
		out = PullStatus{Kind: kind, Status: StatusFailed, Code: code, Message: msg}
	}
	if full && fetched {
		switch {
		case out.Status == StatusPulled:
			m.deferSweep(lockKey, nil)
		case out.Status == StatusFailed && hadHighWater && out.Code != CodePermissionDenied:
			// The sweep may not fit its budget; let deltas run meanwhile.
			until := m.now().Add(sweepBackoff)
			m.deferSweep(lockKey, &until)
		}
	}
	return out
}

func saveState(ctx context.Context, q *db.Queries, s db.UserSyncState) error {
	if err := q.UpsertSyncState(ctx, db.UpsertSyncStateParams{
		CompanyID: s.CompanyID, UserID: s.UserID, ProjectID: s.ProjectID, Kind: s.Kind,
		HighWater: s.HighWater, LastPullAt: s.LastPullAt, LastSweepAt: s.LastSweepAt,
		LastRequestedAt: s.LastRequestedAt, LastError: s.LastError,
	}); err != nil {
		return fmt.Errorf("records: save sync state: %w", err)
	}
	return nil
}

// classify maps a token-source or fetch error to a PullStatus code and
// message, as httpapi maps Procore errors: 401 or a rejected refresh is
// reauth_required, 403 is procore_permission_denied, and any other Procore
// API status, an unreachable Procore or a timeout is procore_unavailable.
// A 404 is procore_permission_denied too: classify only sees list pulls,
// and a 404 on a project's list means the project or the tool is not
// visible to this user, as pipeline's lookups treat it. The pull is the
// per-user project-access and per-kind probe; no separate probe runs.
func classify(err error) (code, message string) {
	var apiErr *procore.APIError
	isAPI := errors.As(err, &apiErr)
	var netErr net.Error
	switch {
	case errors.Is(err, auth.ErrReauthRequired), errors.Is(err, procore.ErrUnauthorized), isAPI && apiErr.Status == 401:
		return CodeReauthRequired, "Sign in to Procore again to sync."
	case errors.Is(err, procore.ErrPermissionDenied), isAPI && (apiErr.Status == 403 || apiErr.Status == 404):
		return CodePermissionDenied, "Procore did not allow access to these records."
	case errors.Is(err, procore.ErrUnavailable), isAPI, errors.Is(err, context.DeadlineExceeded), errors.As(err, &netErr):
		return CodeUnavailable, "Procore could not be reached. Try again shortly."
	}
	return CodeInternal, "Sync failed. Try again shortly."
}

// Delta returns up to limit of the user's changes for the project and kind
// after change_seq since, in change_seq order. A limit outside 1 to
// MaxPageLimit is clamped to that range.
func (m *Mirror) Delta(ctx context.Context, userID uuid.UUID, projectID int64, kind Kind, since int64, limit int) (DeltaPage, error) {
	limit = max(1, min(limit, MaxPageLimit))
	uid := pgtype.UUID{Bytes: userID, Valid: true}
	rows, err := m.st.Q.ListRecordDelta(ctx, db.ListRecordDeltaParams{
		UserID: uid, ProjectID: projectID, Kind: string(kind), Since: since, Limit: int32(limit + 1),
	})
	if err != nil {
		return DeltaPage{}, fmt.Errorf("records: list delta: %w", err)
	}
	page := DeltaPage{NextSince: since, Records: []Record{}, DeletedIDs: []int64{}}
	if len(rows) > limit {
		page.HasMore = true
		rows = rows[:limit]
	}
	for _, r := range rows {
		if r.DeletedAt.Valid {
			page.DeletedIDs = append(page.DeletedIDs, r.ProcoreID)
		} else {
			page.Records = append(page.Records, Record{
				ID: r.ProcoreID, Kind: Kind(r.Kind), UpdatedAt: r.ProcoreUpdatedAt.Time,
				ChangeSeq: r.ChangeSeq, Body: json.RawMessage(r.Body),
			})
		}
		page.NextSince = r.ChangeSeq
	}

	state, err := m.st.Q.GetSyncState(ctx, db.GetSyncStateParams{UserID: uid, ProjectID: projectID, Kind: string(kind)})
	switch {
	case errors.Is(err, pgx.ErrNoRows):
	case err != nil:
		return DeltaPage{}, fmt.Errorf("records: get sync state: %w", err)
	default:
		page.SyncedAt = timePtr(state.LastPullAt)
	}
	return page, nil
}

// WarmAll syncs every (user, project, kind) whose user asked for it within
// WarmWindow, each under its own timeout: warmTimeout, or
// firstPullWarmTimeout for a pair that has never pulled successfully (no
// high-water mark and no last pull), at most maxFirstPullsPerTick of which
// run per call; the others wait for a later run. A failed pair is logged
// and the run continues; only failing to list the pairs, or ctx ending, is
// an error. Warming does not count as a request, so idle pairs age out.
func (m *Mirror) WarmAll(ctx context.Context) error {
	targets, err := m.st.Q.ListWarmTargets(ctx, ts(m.now().Add(-WarmWindow)))
	if err != nil {
		return fmt.Errorf("records: list warm targets: %w", err)
	}
	firstPulls := 0
	for _, t := range targets {
		if err := ctx.Err(); err != nil {
			return err
		}
		budget := warmTimeout
		// A project with no records pulls fine but never gets a high-water
		// mark; its last pull keeps it out of the first-pull slots.
		if !t.HighWater.Valid && !t.LastPullAt.Valid {
			if firstPulls >= maxFirstPullsPerTick {
				continue
			}
			firstPulls++
			budget = firstPullWarmTimeout
		}
		pairCtx, cancel := context.WithTimeout(ctx, budget)
		st := m.sync(pairCtx, uuid.UUID(t.UserID.Bytes), t.CompanyID, t.ProjectID, Kind(t.Kind), SyncOpts{}, false)
		cancel()
		if st.Status == StatusFailed {
			m.log.Warn("records warm sync failed", "kind", t.Kind, "company_id", t.CompanyID, "project_id", t.ProjectID, "code", st.Code)
		}
	}
	return nil
}

func ts(t time.Time) pgtype.Timestamptz { return pgtype.Timestamptz{Time: t, Valid: true} }

func timePtr(t pgtype.Timestamptz) *time.Time {
	if !t.Valid {
		return nil
	}
	v := t.Time
	return &v
}
