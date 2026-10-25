package jobs

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/rivertype"

	"github.com/monroeresidential/fieldassistant/api/internal/pipeline"
	"github.com/monroeresidential/fieldassistant/api/internal/store"
	"github.com/monroeresidential/fieldassistant/api/internal/store/db"
	"github.com/monroeresidential/fieldassistant/api/internal/store/storetest"
)

var _ Submitter = (*pipeline.Drafts)(nil)

// fakeSubmitter records RunSubmit and FailSubmit calls.
type fakeSubmitter struct {
	mu     sync.Mutex
	runs   []uuid.UUID
	failed []uuid.UUID
	err    error
	ran    chan uuid.UUID
}

func newFakeSubmitter() *fakeSubmitter {
	return &fakeSubmitter{ran: make(chan uuid.UUID, 8)}
}

func (f *fakeSubmitter) RunSubmit(_ context.Context, id uuid.UUID) error {
	f.mu.Lock()
	f.runs = append(f.runs, id)
	err := f.err
	f.mu.Unlock()
	f.ran <- id
	return err
}

func (f *fakeSubmitter) FailSubmit(_ context.Context, id uuid.UUID, _ error) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.failed = append(f.failed, id)
	return nil
}

func (f *fakeSubmitter) failedDrafts() []uuid.UUID {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]uuid.UUID(nil), f.failed...)
}

var quiet = slog.New(slog.NewTextHandler(io.Discard, nil))

// enqueueTx inserts draft's submit job for attempt 1 through Queue in its
// own transaction.
func enqueueTx(ctx context.Context, st *store.Store, c *river.Client[pgx.Tx], draft uuid.UUID) (int64, error) {
	return enqueueAttempt(ctx, st, c, draft, 1)
}

func enqueueAttempt(ctx context.Context, st *store.Store, c *river.Client[pgx.Tx], draft uuid.UUID, attempt int) (int64, error) {
	var id int64
	err := st.WithPgxTx(ctx, func(tx pgx.Tx, _ *db.Queries) error {
		var err error
		id, err = NewQueue(c).EnqueueTx(ctx, tx, draft, attempt)
		return err
	})
	return id, err
}

// While River still shows an attempt's job running (its draft already
// settled), the next attempt gets a new job; the same attempt does not.
func TestQueue_EnqueueTx_NewAttemptWhileOldRunning(t *testing.T) {
	st := storetest.New(t)
	c, err := NewClient(st.Pool, newFakeSubmitter(), &fakeWarmer{}, quiet)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	ctx := context.Background()
	draft := uuid.New()
	first, err := enqueueAttempt(ctx, st, c, draft, 1)
	if err != nil {
		t.Fatalf("enqueue attempt 1: %v", err)
	}
	if _, err := st.Pool.Exec(ctx, `update river_job set state = 'running', attempt = 1, attempted_at = now() where id = $1`, first); err != nil {
		t.Fatalf("mark running: %v", err)
	}
	if again, err := enqueueAttempt(ctx, st, c, draft, 1); err != nil || again != first {
		t.Fatalf("attempt 1 again = %d, %v; want the running job %d", again, err, first)
	}
	second, err := enqueueAttempt(ctx, st, c, draft, 2)
	if err != nil || second == first {
		t.Fatalf("attempt 2 = %d, %v; want a new job", second, err)
	}
	js, err := Status(ctx, c, second)
	if err != nil || js.DraftID != draft || js.Status != StatusQueued {
		t.Fatalf("attempt 2 status = %+v, %v", js, err)
	}
}

// River keeps finished jobs for a week, so a client polling late finds them.
func TestRiverConfig_RetainsFinishedJobsForAWeek(t *testing.T) {
	cfg := riverConfig(river.NewWorkers(), newFakeSubmitter(), quiet)
	if cfg.CompletedJobRetentionPeriod != 7*24*time.Hour || cfg.DiscardedJobRetentionPeriod != 7*24*time.Hour {
		t.Errorf("retention completed %s discarded %s, want 168h", cfg.CompletedJobRetentionPeriod, cfg.DiscardedJobRetentionPeriod)
	}
}

// A job inserted in a transaction that rolls back does not exist.
func TestQueue_EnqueueTx_RolledBack_NoJob(t *testing.T) {
	st := storetest.New(t)
	c, err := NewClient(st.Pool, newFakeSubmitter(), &fakeWarmer{}, quiet)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	ctx := context.Background()
	var id int64
	rollback := errors.New("rollback")
	err = st.WithPgxTx(ctx, func(tx pgx.Tx, _ *db.Queries) error {
		var err error
		if id, err = NewQueue(c).EnqueueTx(ctx, tx, uuid.New(), 1); err != nil {
			return err
		}
		return rollback
	})
	if !errors.Is(err, rollback) {
		t.Fatalf("WithPgxTx = %v", err)
	}
	if _, err := Status(ctx, c, id); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Status of rolled-back job = %v, want ErrNotFound", err)
	}
}

func TestQueue_JobFinished(t *testing.T) {
	st := storetest.New(t)
	c, err := NewClient(st.Pool, newFakeSubmitter(), &fakeWarmer{}, quiet)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	ctx := context.Background()
	q := NewQueue(c)
	draft := uuid.New()
	id, err := enqueueTx(ctx, st, c, draft)
	if err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	if done, err := q.JobFinished(ctx, id); err != nil || done {
		t.Fatalf("queued job: finished = %v, %v; want false", done, err)
	}
	if _, err := c.JobCancel(ctx, id); err != nil {
		t.Fatalf("JobCancel: %v", err)
	}
	if done, err := q.JobFinished(ctx, id); err != nil || !done {
		t.Fatalf("cancelled job: finished = %v, %v; want true", done, err)
	}
	if done, err := q.JobFinished(ctx, 987654); err != nil || !done {
		t.Fatalf("missing job: finished = %v, %v; want true", done, err)
	}
	// A new job for the same draft can be inserted once the old one is done.
	again, err := enqueueTx(ctx, st, c, draft)
	if err != nil || again == id {
		t.Fatalf("enqueue after cancel = %d, %v", again, err)
	}
}

func TestEnqueue_SameDraftWhileActive_ReturnsExistingJob(t *testing.T) {
	st := storetest.New(t)
	c, err := NewClient(st.Pool, newFakeSubmitter(), &fakeWarmer{}, quiet)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	ctx := context.Background()
	draft := uuid.New()

	first, err := enqueueTx(ctx, st, c, draft)
	if err != nil {
		t.Fatalf("Enqueue: %v", err)
	}
	again, err := enqueueTx(ctx, st, c, draft)
	if err != nil {
		t.Fatalf("second Enqueue: %v", err)
	}
	if again != first {
		t.Errorf("second Enqueue = job %d, want existing %d", again, first)
	}
	other, err := enqueueTx(ctx, st, c, uuid.New())
	if err != nil {
		t.Fatalf("other Enqueue: %v", err)
	}
	if other == first {
		t.Error("another draft got the same job")
	}

	st1, err := Status(ctx, c, first)
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if st1 != (JobState{ID: first, Status: StatusQueued, DraftID: draft}) {
		t.Errorf("Status = %+v", st1)
	}
}

func TestStatus_UnknownJob_NotFound(t *testing.T) {
	st := storetest.New(t)
	c, err := NewClient(st.Pool, newFakeSubmitter(), &fakeWarmer{}, quiet)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	if _, err := Status(context.Background(), c, 987654); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Status = %v, want ErrNotFound", err)
	}
}

func TestWorker_RunsSubmitAndCompletes(t *testing.T) {
	st := storetest.New(t)
	fs := newFakeSubmitter()
	c, err := NewClient(st.Pool, fs, &fakeWarmer{}, quiet)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	ctx := context.Background()
	if err := c.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() {
		if err := c.Stop(context.Background()); err != nil {
			t.Errorf("Stop: %v", err)
		}
	})
	draft := uuid.New()
	jobID, err := enqueueTx(ctx, st, c, draft)
	if err != nil {
		t.Fatalf("Enqueue: %v", err)
	}

	select {
	case got := <-fs.ran:
		if got != draft {
			t.Fatalf("RunSubmit(%s), want %s", got, draft)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("worker did not run within 10s")
	}
	deadline := time.Now().Add(10 * time.Second)
	for {
		s, err := Status(ctx, c, jobID)
		if err != nil {
			t.Fatalf("Status: %v", err)
		}
		if s.Status == StatusSucceeded {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("status = %q after 10s", s.Status)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func TestErrorHandler_FailsDraftOnlyOnFinalAttempt(t *testing.T) {
	fs := newFakeSubmitter()
	h := &errorHandler{s: fs, log: quiet}
	draft := uuid.New()
	args, _ := json.Marshal(SubmitArgs{DraftID: draft})
	ctx := context.Background()

	h.HandleError(ctx, &rivertype.JobRow{ID: 1, Kind: KindDraftSubmit, Attempt: 3, MaxAttempts: MaxAttempts, EncodedArgs: args}, errors.New("503"))
	if got := fs.failedDrafts(); len(got) != 0 {
		t.Fatalf("FailSubmit after a non-final attempt: %v", got)
	}
	h.HandleError(ctx, &rivertype.JobRow{ID: 1, Kind: KindDraftSubmit, Attempt: 4, MaxAttempts: MaxAttempts, EncodedArgs: args}, errors.New("503"))
	if got := fs.failedDrafts(); len(got) != 1 || got[0] != draft {
		t.Fatalf("FailSubmit calls = %v, want [%s]", got, draft)
	}
	h.HandlePanic(ctx, &rivertype.JobRow{ID: 1, Kind: KindDraftSubmit, Attempt: 4, MaxAttempts: MaxAttempts, EncodedArgs: args}, "boom", "trace")
	if got := fs.failedDrafts(); len(got) != 2 {
		t.Fatalf("FailSubmit after final panic: %v", got)
	}
}

func TestStatusFor_MapsRiverStates(t *testing.T) {
	want := map[rivertype.JobState]string{
		rivertype.JobStateAvailable: StatusQueued,
		rivertype.JobStateScheduled: StatusQueued,
		rivertype.JobStateRetryable: StatusQueued,
		rivertype.JobStatePending:   StatusQueued,
		rivertype.JobStateRunning:   StatusRunning,
		rivertype.JobStateCompleted: StatusSucceeded,
		rivertype.JobStateDiscarded: StatusFailed,
		rivertype.JobStateCancelled: StatusFailed,
	}
	for _, s := range rivertype.JobStates() {
		if got := statusFor(s); got != want[s] {
			t.Errorf("statusFor(%s) = %q, want %q", s, got, want[s])
		}
	}
}

// A warm job is skipped while one is active, but a completed one (kept for a
// week) must not block the next tick.
func TestWarmJob_UniqueWhileActiveOnly(t *testing.T) {
	st := storetest.New(t)
	c, err := NewClient(st.Pool, newFakeSubmitter(), &fakeWarmer{}, quiet)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	ctx := context.Background()
	args, opts := warmConstructor()
	first, err := c.Insert(ctx, args, opts)
	if err != nil || first.UniqueSkippedAsDuplicate {
		t.Fatalf("first insert = %+v, %v", first, err)
	}
	second, err := c.Insert(ctx, args, opts)
	if err != nil || !second.UniqueSkippedAsDuplicate || second.Job.ID != first.Job.ID {
		t.Fatalf("second insert = %+v, %v; want skipped duplicate of %d", second, err, first.Job.ID)
	}
	if _, err := st.Pool.Exec(ctx, `update river_job set state = 'completed', finalized_at = now() where id = $1`, first.Job.ID); err != nil {
		t.Fatalf("mark completed: %v", err)
	}
	third, err := c.Insert(ctx, args, opts)
	if err != nil || third.UniqueSkippedAsDuplicate || third.Job.ID == first.Job.ID {
		t.Fatalf("third insert = %+v, %v; want a new job", third, err)
	}
}
