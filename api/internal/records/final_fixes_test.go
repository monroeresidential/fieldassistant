package records_test

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/monroeresidential/fieldassistant/api/internal/procore"
	"github.com/monroeresidential/fieldassistant/api/internal/records"
	"github.com/monroeresidential/fieldassistant/api/internal/records/recordstest"
	"github.com/monroeresidential/fieldassistant/api/internal/store"
	"github.com/monroeresidential/fieldassistant/api/internal/store/db"
	"github.com/monroeresidential/fieldassistant/api/internal/store/storetest"
)

// poolTokens reads the database through the mirror's own pool before
// answering, as auth.TokenSource does when it loads the stored tokens.
type poolTokens struct{ st *store.Store }

func (p poolTokens) AccessToken(ctx context.Context, _ uuid.UUID) (string, error) {
	var one int
	if err := p.st.Pool.QueryRow(ctx, "select 1").Scan(&one); err != nil {
		return "", fmt.Errorf("token read: %w", err)
	}
	return "tok", nil
}

// TestSync_TokenFetchedBeforeTransaction runs a Sync on a pool of one
// connection with a token source that needs that pool. If the token were
// read inside the sync transaction, the read would wait for the only
// connection, which the transaction holds, until the context ends.
func TestSync_TokenFetchedBeforeTransaction(t *testing.T) {
	base := storetest.New(t)
	ctx := context.Background()
	u, err := base.Q.UpsertUserByProcoreID(ctx, db.UpsertUserByProcoreIDParams{ProcoreUserID: 503, Email: "one@example.com", Name: "One"})
	if err != nil {
		t.Fatal(err)
	}
	url := os.Getenv("TEST_DATABASE_URL")
	sep := "?"
	if strings.Contains(url, "?") {
		sep = "&"
	}
	small, err := store.Open(ctx, url+sep+"pool_max_conns=1")
	if err != nil {
		t.Fatalf("open one-connection store: %v", err)
	}
	defer small.Close()

	f := &recordstest.FakeFetcher{Script: []func(*time.Time) ([]procore.RawRecord, error){
		recordstest.Returns(rec(1, t0.Add(-time.Hour), "a")),
	}}
	m := records.NewMirror(small, map[records.Kind]records.Fetcher{records.KindRFI: f}, poolTokens{small}, func() time.Time { return t0 }, quiet)
	syncCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	got := m.Sync(syncCtx, uuid.UUID(u.ID.Bytes), companyID, projectID, records.KindRFI, records.SyncOpts{})
	wantStatus(t, got, "pulled", "")
}

// TestSync_TokenFailureIsRecorded checks a token-source failure, now read
// before the transaction, is still written to last_error.
func TestSync_TokenFailureIsRecorded(t *testing.T) {
	e := newEnv(t)
	m := records.NewMirror(e.st, map[records.Kind]records.Fetcher{records.KindRFI: e.f},
		fakeTokens{err: fmt.Errorf("%w: dial tcp", procore.ErrUnavailable)}, e.clock.Now, quiet)
	got := m.Sync(context.Background(), e.user, companyID, projectID, records.KindRFI, records.SyncOpts{})
	wantStatus(t, got, "failed", "procore_unavailable")
	if s := e.state(t); !s.LastError.Valid || !strings.HasPrefix(s.LastError.String, "procore_unavailable: ") {
		t.Errorf("last_error = %v, want the token failure", s.LastError)
	}
	if e.f.CallCount() != 0 {
		t.Errorf("fetcher called %d times without a token", e.f.CallCount())
	}
}

// TestSync_FreshPullIgnoresTokenFailure checks a pull inside the freshness
// window is still fresh when the token source fails, as before the token
// moved out of the transaction.
func TestSync_FreshPullIgnoresTokenFailure(t *testing.T) {
	e := newEnv(t, recordstest.Returns(rec(1, t0.Add(-time.Hour), "a")))
	wantStatus(t, e.sync(t, records.SyncOpts{}), "pulled", "")
	m := records.NewMirror(e.st, map[records.Kind]records.Fetcher{records.KindRFI: e.f},
		fakeTokens{err: fmt.Errorf("%w: dial tcp", procore.ErrUnavailable)}, e.clock.Now, quiet)
	got := m.Sync(context.Background(), e.user, companyID, projectID, records.KindRFI, records.SyncOpts{})
	wantStatus(t, got, "fresh", "")
}

func TestSync_NotFoundIsPermissionDenied(t *testing.T) {
	e := newEnv(t,
		recordstest.Returns(rec(1, t0.Add(-time.Hour), "a")),
		recordstest.Fails(&procore.APIError{Status: 404, Body: "not found"}),
	)
	wantStatus(t, e.sync(t, records.SyncOpts{}), "pulled", "")
	before := e.delta(t, 0, 100)

	e.clock.Advance(2 * time.Minute)
	wantStatus(t, e.sync(t, records.SyncOpts{}), "failed", "procore_permission_denied")
	after := e.delta(t, before.NextSince, 100)
	if len(after.Records) != 0 || !slices.Equal(after.DeletedIDs, []int64{1}) {
		t.Errorf("after a 404 = records %v deleted %v, want record 1 deleted", ids(after.Records), after.DeletedIDs)
	}
	if s := e.state(t); s.HighWater.Valid || s.LastSweepAt.Valid {
		t.Errorf("cursors kept after a 404: high_water %v last_sweep_at %v", s.HighWater, s.LastSweepAt)
	}
}

// recWithAttachment is rec with one question attachment served from a
// signed url, as Procore returns it.
func recWithAttachment(id int64, updated time.Time, subject, sig string) procore.RawRecord {
	body, _ := json.Marshal(map[string]any{
		"id": id, "subject": subject, "updated_at": updated.Format(time.RFC3339),
		"questions": []any{map[string]any{
			"id":          id * 10,
			"attachments": []any{map[string]any{"id": id * 100, "name": "photo", "url": "https://files.example.com/photo.jpg?sig=" + sig}},
		}},
	})
	return procore.RawRecord{ID: id, UpdatedAt: updated, Body: body}
}

func TestSync_SweepWithRotatedAttachmentURLsKeepsCursor(t *testing.T) {
	hw := t0.Add(-time.Hour)
	e := newEnv(t,
		recordstest.Returns(recWithAttachment(1, hw, "a", "first")),
		recordstest.Returns(recWithAttachment(1, hw, "a", "second")),
	)
	wantStatus(t, e.sync(t, records.SyncOpts{}), "pulled", "")
	before := e.delta(t, 0, 100)

	e.clock.Advance(records.SweepInterval + time.Minute)
	wantStatus(t, e.sync(t, records.SyncOpts{}), "pulled", "")
	if e.f.Calls[1] != nil {
		t.Fatalf("second pull since = %v, want a full sweep", *e.f.Calls[1])
	}
	if after := e.delta(t, before.NextSince, 100); len(after.Records)+len(after.DeletedIDs) != 0 {
		t.Errorf("a sweep whose only change is re-signed attachment urls moved the cursor: records %v deleted %v", ids(after.Records), after.DeletedIDs)
	}
}

func TestSync_FailedSweepFallsBackToDelta(t *testing.T) {
	hw := t0.Add(-time.Hour)
	e := newEnv(t,
		recordstest.Returns(rec(1, hw, "a")),
		recordstest.Fails(context.DeadlineExceeded),
		recordstest.Returns(rec(1, hw, "a")),
		recordstest.Returns(rec(1, hw, "a")),
	)
	wantStatus(t, e.sync(t, records.SyncOpts{}), "pulled", "")

	e.clock.Advance(records.SweepInterval + time.Minute)
	wantStatus(t, e.sync(t, records.SyncOpts{}), "failed", "procore_unavailable")
	if e.f.Calls[1] != nil {
		t.Fatalf("due sweep since = %v, want nil (full)", *e.f.Calls[1])
	}

	e.clock.Advance(2 * time.Minute)
	wantStatus(t, e.sync(t, records.SyncOpts{}), "pulled", "")
	if e.f.Calls[2] == nil {
		t.Fatal("pull after a failed sweep was full again, want a delta")
	}
	if want := hw.Add(-records.Overlap); !e.f.Calls[2].Equal(want) {
		t.Errorf("delta since = %v, want %v", *e.f.Calls[2], want)
	}

	// Once the backoff ends, the sweep is tried again.
	e.clock.Advance(15 * time.Minute)
	wantStatus(t, e.sync(t, records.SyncOpts{}), "pulled", "")
	if e.f.Calls[3] != nil {
		t.Errorf("pull after the backoff since = %v, want nil (full)", *e.f.Calls[3])
	}
}

// TestSync_ExplicitFullIgnoresSweepBackoff checks a user's full sync still
// sweeps while the automatic sweep is backed off.
func TestSync_ExplicitFullIgnoresSweepBackoff(t *testing.T) {
	hw := t0.Add(-time.Hour)
	e := newEnv(t,
		recordstest.Returns(rec(1, hw, "a")),
		recordstest.Fails(context.DeadlineExceeded),
		recordstest.Returns(rec(1, hw, "a")),
	)
	wantStatus(t, e.sync(t, records.SyncOpts{}), "pulled", "")
	e.clock.Advance(records.SweepInterval + time.Minute)
	wantStatus(t, e.sync(t, records.SyncOpts{}), "failed", "procore_unavailable")
	e.clock.Advance(time.Minute)
	wantStatus(t, e.sync(t, records.SyncOpts{Force: true, Full: true}), "pulled", "")
	if e.f.Calls[2] != nil {
		t.Errorf("explicit full sync since = %v, want nil", *e.f.Calls[2])
	}
}

// seedWarmPair stores a sync state for project requested at requested.
// With warm set it has a high-water mark and a recent sweep, so its next
// pull is a delta; otherwise it is a first pull.
func (e *env) seedWarmPair(t *testing.T, project int64, requested time.Time, warm bool) {
	t.Helper()
	p := db.UpsertSyncStateParams{
		CompanyID: companyID, UserID: pgtype.UUID{Bytes: e.user, Valid: true}, ProjectID: project,
		Kind: string(records.KindRFI), LastRequestedAt: pgtype.Timestamptz{Time: requested, Valid: true},
	}
	if warm {
		p.HighWater = pgtype.Timestamptz{Time: t0.Add(-time.Hour), Valid: true}
		p.LastSweepAt = pgtype.Timestamptz{Time: t0.Add(-time.Hour), Valid: true}
		p.LastPullAt = pgtype.Timestamptz{Time: t0.Add(-10 * time.Minute), Valid: true}
	}
	if err := e.st.Q.UpsertSyncState(context.Background(), p); err != nil {
		t.Fatalf("seed sync state: %v", err)
	}
}

func TestWarmAll_FirstPullGetsLongerBudget(t *testing.T) {
	e := newEnv(t,
		recordstest.Returns(rec(1, t0.Add(-time.Hour), "a")),
		recordstest.Returns(rec(1, t0.Add(-time.Hour), "a")),
	)
	// Targets come back most recently requested first: the warm pair, then
	// the first pull.
	e.seedWarmPair(t, projectID, t0.Add(-time.Minute), true)
	e.seedWarmPair(t, projectID+1, t0.Add(-time.Hour), false)

	start := time.Now()
	if err := e.m.WarmAll(context.Background()); err != nil {
		t.Fatalf("WarmAll: %v", err)
	}
	if len(e.f.Deadlines) != 2 {
		t.Fatalf("fetcher called %d times, want 2", len(e.f.Deadlines))
	}
	if e.f.Calls[0] == nil || e.f.Calls[1] != nil {
		t.Fatalf("calls = %v, want a delta then a full pull", e.f.Calls)
	}
	if budget := e.f.Deadlines[0].Sub(start); budget > 31*time.Second {
		t.Errorf("warm pair budget %v, want at most 30s", budget)
	}
	if budget := e.f.Deadlines[1].Sub(start); budget < 110*time.Second || budget > 121*time.Second {
		t.Errorf("first-pull budget %v, want about 2m", budget)
	}
}

func TestWarmAll_AtMostTwoFirstPullsPerTick(t *testing.T) {
	e := newEnv(t,
		recordstest.Returns(rec(1, t0.Add(-time.Hour), "a")),
		recordstest.Returns(rec(1, t0.Add(-time.Hour), "a")),
		recordstest.Returns(rec(1, t0.Add(-time.Hour), "a")),
		recordstest.Returns(rec(1, t0.Add(-time.Hour), "a")),
	)
	for i := int64(0); i < 3; i++ {
		e.seedWarmPair(t, projectID+i, t0.Add(-time.Duration(i+1)*time.Minute), false)
	}
	e.seedWarmPair(t, projectID+3, t0.Add(-time.Hour), true)

	if err := e.m.WarmAll(context.Background()); err != nil {
		t.Fatalf("WarmAll: %v", err)
	}
	var full, delta int
	for _, c := range e.f.Calls {
		if c == nil {
			full++
		} else {
			delta++
		}
	}
	if full != 2 || delta != 1 {
		t.Errorf("full pulls %d, deltas %d, want 2 and 1 (the third first pull waits a tick)", full, delta)
	}
}
