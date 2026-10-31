package records_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/monroeresidential/fieldassistant/api/internal/auth"
	"github.com/monroeresidential/fieldassistant/api/internal/procore"
	"github.com/monroeresidential/fieldassistant/api/internal/records"
	"github.com/monroeresidential/fieldassistant/api/internal/records/recordstest"
	"github.com/monroeresidential/fieldassistant/api/internal/store"
	"github.com/monroeresidential/fieldassistant/api/internal/store/db"
	"github.com/monroeresidential/fieldassistant/api/internal/store/storetest"
)

const (
	companyID = int64(10)
	projectID = int64(20)
)

var quiet = slog.New(slog.NewTextHandler(io.Discard, nil))

var t0 = time.Date(2026, 10, 25, 12, 0, 0, 0, time.UTC)

type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *clock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

type fakeTokens struct{ err error }

func (f fakeTokens) AccessToken(context.Context, uuid.UUID) (string, error) {
	if f.err != nil {
		return "", f.err
	}
	return "tok", nil
}

type env struct {
	st    *store.Store
	m     *records.Mirror
	f     *recordstest.FakeFetcher
	clock *clock
	user  uuid.UUID
}

func newEnv(t *testing.T, script ...func(*time.Time) ([]procore.RawRecord, error)) *env {
	t.Helper()
	st := storetest.New(t)
	u, err := st.Q.UpsertUserByProcoreID(context.Background(), db.UpsertUserByProcoreIDParams{
		ProcoreUserID: 501, Email: "pm@example.com", Name: "PM",
	})
	if err != nil {
		t.Fatalf("upsert user: %v", err)
	}
	f := &recordstest.FakeFetcher{Script: script}
	c := &clock{t: t0}
	m := records.NewMirror(st, map[records.Kind]records.Fetcher{records.KindRFI: f}, fakeTokens{}, c.Now, quiet)
	return &env{st: st, m: m, f: f, clock: c, user: uuid.UUID(u.ID.Bytes)}
}

func rec(id int64, updated time.Time, subject string) procore.RawRecord {
	body, _ := json.Marshal(map[string]any{"id": id, "subject": subject, "updated_at": updated.Format(time.RFC3339)})
	return procore.RawRecord{ID: id, UpdatedAt: updated, Body: body}
}

func (e *env) sync(t *testing.T, opts records.SyncOpts) records.PullStatus {
	t.Helper()
	return e.m.Sync(context.Background(), e.user, companyID, projectID, records.KindRFI, opts)
}

func (e *env) state(t *testing.T) db.UserSyncState {
	t.Helper()
	s, err := e.st.Q.GetSyncState(context.Background(), db.GetSyncStateParams{
		UserID: pgtype.UUID{Bytes: e.user, Valid: true}, ProjectID: projectID, Kind: string(records.KindRFI),
	})
	if err != nil {
		t.Fatalf("get sync state: %v", err)
	}
	return s
}

func (e *env) delta(t *testing.T, since int64, limit int) records.DeltaPage {
	t.Helper()
	p, err := e.m.Delta(context.Background(), e.user, projectID, records.KindRFI, since, limit)
	if err != nil {
		t.Fatalf("delta: %v", err)
	}
	return p
}

func ids(rs []records.Record) []int64 {
	out := make([]int64, len(rs))
	for i, r := range rs {
		out[i] = r.ID
	}
	return out
}

func wantStatus(t *testing.T, got records.PullStatus, status, code string) {
	t.Helper()
	if got.Status != status || got.Code != code {
		t.Fatalf("status = %q code %q (%q), want %q code %q", got.Status, got.Code, got.Message, status, code)
	}
	if got.Kind != records.KindRFI {
		t.Errorf("kind = %q, want rfi", got.Kind)
	}
}

func TestParseKind(t *testing.T) {
	if k, ok := records.ParseKind("rfi"); !ok || k != records.KindRFI {
		t.Errorf("ParseKind(rfi) = %q, %v", k, ok)
	}
	if _, ok := records.ParseKind("observation"); ok {
		t.Error("ParseKind(observation) ok, want false")
	}
}

func TestSync_FirstPullIsFullAndSetsHighWater(t *testing.T) {
	u1, u2, u3 := t0.Add(-3*time.Hour), t0.Add(-1*time.Hour), t0.Add(-2*time.Hour)
	e := newEnv(t, recordstest.Returns(rec(1, u1, "a"), rec(2, u2, "b"), rec(3, u3, "c")))

	got := e.sync(t, records.SyncOpts{})
	wantStatus(t, got, "pulled", "")
	if got.PulledAt == nil || !got.PulledAt.Equal(t0) {
		t.Errorf("PulledAt = %v, want %v", got.PulledAt, t0)
	}
	if len(e.f.Calls) != 1 || e.f.Calls[0] != nil {
		t.Fatalf("calls = %v, want one full fetch (since nil)", e.f.Calls)
	}
	s := e.state(t)
	if !s.HighWater.Valid || !s.HighWater.Time.Equal(u2) {
		t.Errorf("high_water = %v, want %v", s.HighWater, u2)
	}
	if !s.LastSweepAt.Valid || !s.LastSweepAt.Time.Equal(t0) {
		t.Errorf("last_sweep_at = %v, want %v", s.LastSweepAt, t0)
	}
	if !s.LastPullAt.Valid || !s.LastPullAt.Time.Equal(t0) {
		t.Errorf("last_pull_at = %v, want %v", s.LastPullAt, t0)
	}
	if !s.LastRequestedAt.Valid || !s.LastRequestedAt.Time.Equal(t0) {
		t.Errorf("last_requested_at = %v, want %v", s.LastRequestedAt, t0)
	}
	if s.LastError.Valid {
		t.Errorf("last_error = %q, want null", s.LastError.String)
	}
	if s.CompanyID != companyID {
		t.Errorf("company_id = %d, want %d", s.CompanyID, companyID)
	}

	p := e.delta(t, 0, 100)
	if got := ids(p.Records); !slices.Equal(got, []int64{1, 2, 3}) {
		t.Errorf("delta ids = %v, want [1 2 3]", got)
	}
	if len(p.DeletedIDs) != 0 || p.HasMore {
		t.Errorf("deleted = %v hasMore = %v, want none and false", p.DeletedIDs, p.HasMore)
	}
	if p.SyncedAt == nil || !p.SyncedAt.Equal(t0) {
		t.Errorf("SyncedAt = %v, want %v", p.SyncedAt, t0)
	}
	r := p.Records[0]
	if r.Kind != records.KindRFI || !r.UpdatedAt.Equal(u1) || r.Deleted || r.ChangeSeq == 0 {
		t.Errorf("record 1 = %+v", r)
	}
	var body map[string]any
	if err := json.Unmarshal(r.Body, &body); err != nil || body["subject"] != "a" {
		t.Errorf("record 1 body = %s (%v)", r.Body, err)
	}
	if p.NextSince != p.Records[2].ChangeSeq {
		t.Errorf("NextSince = %d, want last change_seq %d", p.NextSince, p.Records[2].ChangeSeq)
	}
}

func TestSync_WithinFreshnessWindowIsFresh(t *testing.T) {
	e := newEnv(t, recordstest.Returns(rec(1, t0.Add(-time.Hour), "a")))
	wantStatus(t, e.sync(t, records.SyncOpts{}), "pulled", "")

	e.clock.Advance(10 * time.Second)
	got := e.sync(t, records.SyncOpts{})
	wantStatus(t, got, "fresh", "")
	if got.PulledAt == nil || !got.PulledAt.Equal(t0) {
		t.Errorf("PulledAt = %v, want first pull %v", got.PulledAt, t0)
	}
	if n := len(e.f.Calls); n != 1 {
		t.Errorf("fetcher called %d times, want 1", n)
	}
	s := e.state(t)
	if !s.LastRequestedAt.Time.Equal(t0.Add(10 * time.Second)) {
		t.Errorf("last_requested_at = %v, want the second request", s.LastRequestedAt.Time)
	}
	if !s.LastPullAt.Time.Equal(t0) {
		t.Errorf("last_pull_at = %v, want unchanged %v", s.LastPullAt.Time, t0)
	}
}

func TestSync_ForceBypassesWindow(t *testing.T) {
	e := newEnv(t,
		recordstest.Returns(rec(1, t0.Add(-time.Hour), "a")),
		recordstest.Returns(rec(1, t0.Add(-time.Hour), "a2")),
	)
	wantStatus(t, e.sync(t, records.SyncOpts{}), "pulled", "")
	e.clock.Advance(10 * time.Second)
	got := e.sync(t, records.SyncOpts{Force: true})
	wantStatus(t, got, "pulled", "")
	if n := len(e.f.Calls); n != 2 {
		t.Fatalf("fetcher called %d times, want 2", n)
	}
	if !e.state(t).LastPullAt.Time.Equal(t0.Add(10 * time.Second)) {
		t.Errorf("last_pull_at not moved by the forced pull")
	}
}

func TestSync_DeltaUsesHighWaterMinusOverlap(t *testing.T) {
	hw := t0.Add(-30 * time.Minute)
	newer := t0.Add(90 * time.Minute)
	e := newEnv(t,
		recordstest.Returns(rec(1, t0.Add(-time.Hour), "a"), rec(2, hw, "b")),
		recordstest.Returns(rec(3, newer, "c")),
	)
	wantStatus(t, e.sync(t, records.SyncOpts{}), "pulled", "")

	e.clock.Advance(2 * time.Hour)
	wantStatus(t, e.sync(t, records.SyncOpts{}), "pulled", "")
	if len(e.f.Calls) != 2 || e.f.Calls[1] == nil {
		t.Fatalf("calls = %v, want a second, delta fetch", e.f.Calls)
	}
	if want := hw.Add(-records.Overlap); !e.f.Calls[1].Equal(want) {
		t.Errorf("since = %v, want high_water - overlap = %v", *e.f.Calls[1], want)
	}
	s := e.state(t)
	if !s.HighWater.Time.Equal(newer) {
		t.Errorf("high_water = %v, want %v", s.HighWater.Time, newer)
	}
	if !s.LastSweepAt.Time.Equal(t0) {
		t.Errorf("last_sweep_at = %v, want unchanged %v after a delta pull", s.LastSweepAt.Time, t0)
	}
	// A delta pull never sweeps: records 1 and 2 stay live.
	p := e.delta(t, 0, 100)
	if got := ids(p.Records); !slices.Equal(got, []int64{1, 2, 3}) || len(p.DeletedIDs) != 0 {
		t.Errorf("delta ids = %v deleted %v, want [1 2 3] and none", got, p.DeletedIDs)
	}
}

func TestSync_StaleSweepIsFull(t *testing.T) {
	e := newEnv(t,
		recordstest.Returns(rec(1, t0.Add(-time.Hour), "a")),
		recordstest.Returns(rec(1, t0.Add(-time.Hour), "a")),
	)
	wantStatus(t, e.sync(t, records.SyncOpts{}), "pulled", "")
	e.clock.Advance(records.SweepInterval + time.Minute)
	wantStatus(t, e.sync(t, records.SyncOpts{}), "pulled", "")
	if e.f.Calls[1] != nil {
		t.Errorf("since = %v, want nil (full) once the last sweep is older than SweepInterval", *e.f.Calls[1])
	}
	if s := e.state(t); !s.LastSweepAt.Time.Equal(e.clock.Now()) {
		t.Errorf("last_sweep_at = %v, want %v", s.LastSweepAt.Time, e.clock.Now())
	}
}

func TestSync_FetchErrorKeepsHighWater(t *testing.T) {
	hw := t0.Add(-time.Hour)
	e := newEnv(t,
		recordstest.Returns(rec(1, hw, "a")),
		recordstest.Fails(&procore.APIError{Status: 502, Body: "bad gateway"}),
	)
	wantStatus(t, e.sync(t, records.SyncOpts{}), "pulled", "")
	before := e.delta(t, 0, 100)

	e.clock.Advance(2 * time.Hour)
	got := e.sync(t, records.SyncOpts{})
	wantStatus(t, got, "failed", "procore_unavailable")
	if got.Message == "" {
		t.Error("failed status has no message")
	}
	if got.PulledAt == nil || !got.PulledAt.Equal(t0) {
		t.Errorf("PulledAt = %v, want the last good pull %v", got.PulledAt, t0)
	}
	s := e.state(t)
	if !s.HighWater.Time.Equal(hw) {
		t.Errorf("high_water = %v, want unchanged %v", s.HighWater.Time, hw)
	}
	if !s.LastPullAt.Time.Equal(t0) {
		t.Errorf("last_pull_at = %v, want unchanged %v", s.LastPullAt.Time, t0)
	}
	if !s.LastError.Valid || s.LastError.String == "" {
		t.Error("last_error not set")
	}
	if !s.LastRequestedAt.Time.Equal(t0.Add(2 * time.Hour)) {
		t.Errorf("last_requested_at = %v, want the failed request's time", s.LastRequestedAt.Time)
	}
	if after := e.delta(t, before.NextSince, 100); len(after.Records)+len(after.DeletedIDs) != 0 {
		t.Errorf("rows changed after a failed fetch: %+v", after)
	}
}

func TestSync_SuccessClearsLastError(t *testing.T) {
	e := newEnv(t,
		recordstest.Fails(&procore.APIError{Status: 503}),
		recordstest.Returns(rec(1, t0, "a")),
	)
	wantStatus(t, e.sync(t, records.SyncOpts{}), "failed", "procore_unavailable")
	if !e.state(t).LastError.Valid {
		t.Fatal("last_error not set by the failed pull")
	}
	// The failed pull did not set last_pull_at, so the next call retries.
	wantStatus(t, e.sync(t, records.SyncOpts{}), "pulled", "")
	if s := e.state(t); s.LastError.Valid {
		t.Errorf("last_error = %q after a good pull, want null", s.LastError.String)
	}
}

func TestSync_FullSweepSoftDeletesMissing(t *testing.T) {
	a, b := rec(1, t0.Add(-2*time.Hour), "a"), rec(2, t0.Add(-time.Hour), "b")
	e := newEnv(t, recordstest.Returns(a, b), recordstest.Returns(a))
	wantStatus(t, e.sync(t, records.SyncOpts{}), "pulled", "")
	first := e.delta(t, 0, 100)

	e.clock.Advance(time.Minute)
	wantStatus(t, e.sync(t, records.SyncOpts{Full: true}), "pulled", "")
	if e.f.Calls[1] != nil {
		t.Fatalf("Full pull since = %v, want nil", *e.f.Calls[1])
	}
	p := e.delta(t, first.NextSince, 100)
	if !slices.Equal(p.DeletedIDs, []int64{2}) || len(p.Records) != 0 {
		t.Errorf("delta = records %v deleted %v, want deleted [2] only", ids(p.Records), p.DeletedIDs)
	}
}

func TestSync_FullSweepWithNoRecordsDeletesAll(t *testing.T) {
	e := newEnv(t, recordstest.Returns(rec(1, t0.Add(-time.Hour), "a")), recordstest.Returns())
	wantStatus(t, e.sync(t, records.SyncOpts{}), "pulled", "")
	first := e.delta(t, 0, 100)
	hw := e.state(t).HighWater.Time

	e.clock.Advance(time.Minute)
	wantStatus(t, e.sync(t, records.SyncOpts{Full: true}), "pulled", "")
	p := e.delta(t, first.NextSince, 100)
	if !slices.Equal(p.DeletedIDs, []int64{1}) {
		t.Errorf("deleted = %v, want [1] after an empty full fetch", p.DeletedIDs)
	}
	if s := e.state(t); !s.HighWater.Time.Equal(hw) {
		t.Errorf("high_water = %v, want unchanged %v after an empty fetch", s.HighWater.Time, hw)
	}
}

func TestSync_UnchangedBodiesDoNotMoveTheCursor(t *testing.T) {
	a, b := rec(1, t0.Add(-2*time.Hour), "a"), rec(2, t0.Add(-time.Hour), "b")
	e := newEnv(t, recordstest.Returns(a, b), recordstest.Returns(a, b))
	wantStatus(t, e.sync(t, records.SyncOpts{}), "pulled", "")
	first := e.delta(t, 0, 100)

	e.clock.Advance(time.Minute)
	wantStatus(t, e.sync(t, records.SyncOpts{Force: true, Full: true}), "pulled", "")
	p := e.delta(t, first.NextSince, 100)
	if len(p.Records) != 0 || len(p.DeletedIDs) != 0 {
		t.Errorf("delta after identical sweep = %v / %v, want empty", ids(p.Records), p.DeletedIDs)
	}
	if p.NextSince != first.NextSince {
		t.Errorf("NextSince = %d, want unchanged %d on an empty page", p.NextSince, first.NextSince)
	}
}

func TestSync_PermissionDeniedIsFailedNotError(t *testing.T) {
	denied := fmt.Errorf("%w: %w", procore.ErrPermissionDenied, &procore.APIError{Status: 403})
	e := newEnv(t, recordstest.Fails(denied))
	got := e.sync(t, records.SyncOpts{})
	wantStatus(t, got, "failed", "procore_permission_denied")
	if got.PulledAt != nil {
		t.Errorf("PulledAt = %v, want nil before any good pull", got.PulledAt)
	}
	s := e.state(t)
	if !s.LastError.Valid || s.HighWater.Valid || s.LastPullAt.Valid || s.LastSweepAt.Valid {
		t.Errorf("state after a denied first pull = %+v", s)
	}
}

func TestSync_PermissionDeniedPurgesRows(t *testing.T) {
	denied := fmt.Errorf("%w: %w", procore.ErrPermissionDenied, &procore.APIError{Status: 403})
	a, b := rec(1, t0.Add(-2*time.Hour), "a"), rec(2, t0.Add(-time.Hour), "b")
	e := newEnv(t, recordstest.Returns(a, b), recordstest.Fails(denied), recordstest.Returns(a))
	wantStatus(t, e.sync(t, records.SyncOpts{}), "pulled", "")
	before := e.delta(t, 0, 100)
	if len(before.Records) != 2 {
		t.Fatalf("records before denial = %v, want 2", ids(before.Records))
	}

	e.clock.Advance(2 * time.Hour)
	got := e.sync(t, records.SyncOpts{})
	wantStatus(t, got, "failed", "procore_permission_denied")
	if got.PulledAt != nil {
		t.Errorf("PulledAt = %v, want nil once the mirror is purged", got.PulledAt)
	}
	// A client at the old cursor is told both records are gone.
	p := e.delta(t, before.NextSince, 100)
	gone := slices.Clone(p.DeletedIDs)
	slices.Sort(gone)
	if len(p.Records) != 0 || !slices.Equal(gone, []int64{1, 2}) {
		t.Errorf("delta since cursor after denial = records %v deleted %v, want deleted [1 2]", ids(p.Records), p.DeletedIDs)
	}
	if p := e.delta(t, 0, 100); len(p.Records) != 0 || p.SyncedAt != nil {
		t.Errorf("delta since 0 after denial = records %v synced_at %v, want no records and no synced_at", ids(p.Records), p.SyncedAt)
	}
	s := e.state(t)
	if s.HighWater.Valid || s.LastPullAt.Valid || s.LastSweepAt.Valid {
		t.Errorf("state after denial = %+v, want no high_water, last_pull_at or last_sweep_at", s)
	}
	if !s.LastError.Valid || !strings.HasPrefix(s.LastError.String, "procore_permission_denied") {
		t.Errorf("last_error = %q, want procore_permission_denied", s.LastError.String)
	}
	if !s.LastRequestedAt.Time.Equal(t0.Add(2 * time.Hour)) {
		t.Errorf("last_requested_at = %v, want the denied request's time", s.LastRequestedAt.Time)
	}

	// Access is back: the next pull is full.
	wantStatus(t, e.sync(t, records.SyncOpts{}), "pulled", "")
	if e.f.Calls[2] != nil {
		t.Errorf("pull after denial since = %v, want nil (full)", *e.f.Calls[2])
	}
	if p := e.delta(t, 0, 100); !slices.Equal(ids(p.Records), []int64{1}) || !slices.Equal(p.DeletedIDs, []int64{2}) {
		t.Errorf("after access returns = records %v deleted %v, want records [1] deleted [2]", ids(p.Records), p.DeletedIDs)
	}
}

func TestSync_ErrorCodes(t *testing.T) {
	cases := []struct {
		name string
		err  error
		code string
	}{
		{"unauthorized", fmt.Errorf("%w: %w", procore.ErrUnauthorized, &procore.APIError{Status: 401}), "reauth_required"},
		{"bare 403", &procore.APIError{Status: 403}, "procore_permission_denied"},
		{"unavailable", fmt.Errorf("%w: dial tcp", procore.ErrUnavailable), "procore_unavailable"},
		{"timeout", context.DeadlineExceeded, "procore_unavailable"},
		// A 400 or 422 with an errors map decodes as a ValidationError, not
		// an APIError; on a read it is still Procore failing the pull.
		{"400 with errors", fmt.Errorf("list rfis: %w", &procore.ValidationError{Fields: map[string][]string{"filters.updated_at": {"is invalid"}}}), "procore_unavailable"},
		{"other", fmt.Errorf("boom"), "internal"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := newEnv(t, recordstest.Fails(tc.err))
			wantStatus(t, e.sync(t, records.SyncOpts{}), "failed", tc.code)
		})
	}
}

func TestSync_ReauthFromTokenSource(t *testing.T) {
	st := storetest.New(t)
	u, err := st.Q.UpsertUserByProcoreID(context.Background(), db.UpsertUserByProcoreIDParams{ProcoreUserID: 502, Email: "x@example.com", Name: "X"})
	if err != nil {
		t.Fatal(err)
	}
	f := &recordstest.FakeFetcher{}
	m := records.NewMirror(st, map[records.Kind]records.Fetcher{records.KindRFI: f},
		fakeTokens{err: fmt.Errorf("refresh: %w", auth.ErrReauthRequired)}, func() time.Time { return t0 }, quiet)
	got := m.Sync(context.Background(), uuid.UUID(u.ID.Bytes), companyID, projectID, records.KindRFI, records.SyncOpts{})
	wantStatus(t, got, "failed", "reauth_required")
	if len(f.Calls) != 0 {
		t.Errorf("fetcher called %d times without a token", len(f.Calls))
	}
}

func TestSync_UnknownKindFails(t *testing.T) {
	e := newEnv(t)
	got := e.m.Sync(context.Background(), e.user, companyID, projectID, records.Kind("observation"), records.SyncOpts{})
	if got.Status != "failed" || got.Code != "internal" {
		t.Errorf("status = %+v, want failed internal", got)
	}
}

func TestSync_ConcurrentSameKeyPullsOnce(t *testing.T) {
	e := newEnv(t, recordstest.Returns(rec(1, t0.Add(-time.Hour), "a")))
	e.f.Block = make(chan struct{})

	var wg sync.WaitGroup
	results := make([]records.PullStatus, 2)
	for i := range results {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			results[i] = e.sync(t, records.SyncOpts{})
		}(i)
	}
	// Wait until one caller is inside the fetch (holding the lock), give the
	// other time to queue on the lock, then release the fetch.
	deadline := time.Now().Add(5 * time.Second)
	for e.f.CallCount() == 0 {
		if time.Now().After(deadline) {
			close(e.f.Block)
			wg.Wait()
			t.Fatal("no caller reached the fetcher")
		}
		time.Sleep(5 * time.Millisecond)
	}
	time.Sleep(200 * time.Millisecond)
	close(e.f.Block)
	wg.Wait()

	if n := e.f.CallCount(); n != 1 {
		t.Errorf("fetcher called %d times, want 1", n)
	}
	var pulled, fresh int
	for _, r := range results {
		switch r.Status {
		case "pulled":
			pulled++
		case "fresh":
			fresh++
		default:
			t.Errorf("status = %+v, want pulled or fresh", r)
		}
	}
	if pulled != 1 || fresh != 1 {
		t.Errorf("pulled %d fresh %d, want one of each", pulled, fresh)
	}
}

func TestDelta_PagesByChangeSeq(t *testing.T) {
	var recs []procore.RawRecord
	for i := int64(1); i <= 7; i++ {
		recs = append(recs, rec(i, t0.Add(-time.Duration(i)*time.Minute), fmt.Sprint("s", i)))
	}
	e := newEnv(t, recordstest.Returns(recs...))
	wantStatus(t, e.sync(t, records.SyncOpts{}), "pulled", "")

	var all []int64
	since := int64(0)
	wantMore := []bool{true, true, false}
	wantLen := []int{3, 3, 1}
	for i := range wantMore {
		p := e.delta(t, since, 3)
		if len(p.Records) != wantLen[i] || p.HasMore != wantMore[i] {
			t.Fatalf("page %d: %d records hasMore %v, want %d and %v", i, len(p.Records), p.HasMore, wantLen[i], wantMore[i])
		}
		if last := p.Records[len(p.Records)-1].ChangeSeq; p.NextSince != last {
			t.Errorf("page %d: NextSince %d, want last change_seq %d", i, p.NextSince, last)
		}
		if p.NextSince <= since {
			t.Errorf("page %d: NextSince %d did not advance past %d", i, p.NextSince, since)
		}
		all = append(all, ids(p.Records)...)
		since = p.NextSince
	}
	if !slices.Equal(all, []int64{1, 2, 3, 4, 5, 6, 7}) {
		t.Errorf("paged ids = %v, want 1..7 in order", all)
	}
	if p := e.delta(t, since, 3); len(p.Records) != 0 || p.HasMore || p.NextSince != since {
		t.Errorf("past the end: %+v, want empty with NextSince %d", p, since)
	}
}

func TestDelta_NoSyncStateHasNilSyncedAt(t *testing.T) {
	e := newEnv(t)
	p := e.delta(t, 0, 10)
	if p.SyncedAt != nil || len(p.Records) != 0 || p.NextSince != 0 || p.HasMore {
		t.Errorf("empty delta = %+v", p)
	}
}

func TestWarmAll_SyncsRecentPairsOnly(t *testing.T) {
	e := newEnv(t, recordstest.Returns(rec(1, t0.Add(-time.Hour), "a")))
	ctx := context.Background()
	recent, old := t0.Add(-time.Hour), t0.Add(-30*time.Hour)
	for _, s := range []struct {
		project int64
		at      time.Time
	}{{projectID, recent}, {projectID + 1, old}} {
		if err := e.st.Q.UpsertSyncState(ctx, db.UpsertSyncStateParams{
			CompanyID: companyID, UserID: pgtype.UUID{Bytes: e.user, Valid: true}, ProjectID: s.project,
			Kind: string(records.KindRFI), LastRequestedAt: pgtype.Timestamptz{Time: s.at, Valid: true},
		}); err != nil {
			t.Fatalf("seed sync state: %v", err)
		}
	}

	if err := e.m.WarmAll(ctx); err != nil {
		t.Fatalf("WarmAll: %v", err)
	}
	if n := e.f.CallCount(); n != 1 {
		t.Fatalf("fetcher called %d times, want 1", n)
	}
	s := e.state(t)
	if !s.LastPullAt.Valid || !s.LastPullAt.Time.Equal(t0) {
		t.Errorf("recent pair last_pull_at = %v, want %v", s.LastPullAt, t0)
	}
	// Warming is not a request: the pair must still age out of WarmWindow.
	if !s.LastRequestedAt.Time.Equal(recent) {
		t.Errorf("last_requested_at = %v, want unchanged %v", s.LastRequestedAt.Time, recent)
	}
	oldState, err := e.st.Q.GetSyncState(ctx, db.GetSyncStateParams{
		UserID: pgtype.UUID{Bytes: e.user, Valid: true}, ProjectID: projectID + 1, Kind: string(records.KindRFI),
	})
	if err != nil {
		t.Fatal(err)
	}
	if oldState.LastPullAt.Valid {
		t.Errorf("old pair was pulled at %v", oldState.LastPullAt.Time)
	}
}

func TestWarmAll_ContinuesPastFailures(t *testing.T) {
	e := newEnv(t,
		recordstest.Fails(&procore.APIError{Status: 500}),
		recordstest.Returns(rec(1, t0, "a")),
	)
	ctx := context.Background()
	for i, at := range []time.Time{t0.Add(-time.Hour), t0.Add(-2 * time.Hour)} {
		if err := e.st.Q.UpsertSyncState(ctx, db.UpsertSyncStateParams{
			CompanyID: companyID, UserID: pgtype.UUID{Bytes: e.user, Valid: true}, ProjectID: projectID + int64(i),
			Kind: string(records.KindRFI), LastRequestedAt: pgtype.Timestamptz{Time: at, Valid: true},
		}); err != nil {
			t.Fatalf("seed sync state: %v", err)
		}
	}
	if err := e.m.WarmAll(ctx); err != nil {
		t.Fatalf("WarmAll: %v", err)
	}
	if n := e.f.CallCount(); n != 2 {
		t.Errorf("fetcher called %d times, want 2 (a failure must not stop the run)", n)
	}
}
