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
	"github.com/riverqueue/river/rivertype"

	"github.com/monroeresidential/fieldassistant/api/internal/pipeline"
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

func TestEnqueue_SameDraftWhileActive_ReturnsExistingJob(t *testing.T) {
	st := storetest.New(t)
	c, err := NewClient(st.Pool, newFakeSubmitter(), quiet)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	ctx := context.Background()
	draft := uuid.New()

	first, err := Enqueue(ctx, c, draft)
	if err != nil {
		t.Fatalf("Enqueue: %v", err)
	}
	again, err := Enqueue(ctx, c, draft)
	if err != nil {
		t.Fatalf("second Enqueue: %v", err)
	}
	if again != first {
		t.Errorf("second Enqueue = job %d, want existing %d", again, first)
	}
	other, err := Enqueue(ctx, c, uuid.New())
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
	c, err := NewClient(st.Pool, newFakeSubmitter(), quiet)
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
	c, err := NewClient(st.Pool, fs, quiet)
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
	jobID, err := Enqueue(ctx, c, draft)
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
