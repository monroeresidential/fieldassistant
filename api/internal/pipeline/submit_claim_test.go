package pipeline

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/monroeresidential/fieldassistant/api/internal/procore"
)

// A resubmit that lands after the old job failed the draft but before River
// marked that job finished must get a job that will run, not the finishing
// one (River's unique insert still sees it running).
func TestSubmit_AfterFailureBeforeJobFinishes_GetsRunnableJob(t *testing.T) {
	e := newDraftsEnv(t)
	e.fp.Err["CreateRFI"] = procore.ErrPermissionDenied
	d := e.submittable(t, twistedFinal)
	ctx := context.Background()
	q := &riverQueue{}
	first, err := e.drafts.Submit(ctx, e.user, d.ID, q)
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}
	// The job fails the draft; River has not marked it finished yet.
	if err := e.drafts.RunSubmit(ctx, d.ID); err != nil {
		t.Fatalf("RunSubmit: %v", err)
	}
	if got := e.mustGet(t, d.ID); got.Status != StatusFailed || got.Error == nil {
		t.Fatalf("draft after job = status %q error %+v, want failed", got.Status, got.Error)
	}

	delete(e.fp.Err, "CreateRFI")
	e.fp.Created = procore.RFICreated{ID: 501, Number: "214"}
	out, err := e.drafts.Submit(ctx, e.user, d.ID, q)
	if err != nil {
		t.Fatalf("resubmit: %v", err)
	}
	if out.JobID == 0 || out.JobID == first.JobID {
		t.Fatalf("resubmit job = %d, want a new job (old %d is finishing)", out.JobID, first.JobID)
	}
	got := e.mustGet(t, d.ID)
	if got.Status != StatusSubmitting || got.JobID != out.JobID {
		t.Fatalf("draft = status %q job %d, want submitting with job %d", got.Status, got.JobID, out.JobID)
	}
	if got.Error != nil {
		t.Errorf("submitting draft still shows the old error %+v", got.Error)
	}
	// The new job is live, so a second Submit returns it.
	if again, err := e.drafts.Submit(ctx, e.user, d.ID, q); err != nil || again.JobID != out.JobID {
		t.Fatalf("Submit while new job live = %+v, %v; want job %d", again, err, out.JobID)
	}
	if err := e.drafts.RunSubmit(ctx, d.ID); err != nil {
		t.Fatalf("RunSubmit new job: %v", err)
	}
	if got := e.mustGet(t, d.ID); got.Status != StatusSubmitted || got.Result == nil || got.Result.ProcoreID != 501 {
		t.Fatalf("draft = status %q result %+v, want submitted with 501", got.Status, got.Result)
	}
}

// Two drafts of one user with the same subject recover at once and both
// find the same RFI: only one may adopt it.
func TestRunSubmit_Recovery_ConcurrentClaim_OnlyOneWins(t *testing.T) {
	e := newDraftsEnv(t)
	ctx := context.Background()
	e.fp.Err["CreateRFI"] = fmt.Errorf("post rfi: %w", procore.ErrUnavailable)
	a := e.submittable(t, twistedFinal)
	b := e.submittable(t, twistedFinal)
	for _, d := range []Draft{a, b} {
		e.submit(t, d)
		if err := e.drafts.RunSubmit(ctx, d.ID); !errors.Is(err, procore.ErrUnavailable) {
			t.Fatalf("first attempt = %v, want ErrUnavailable", err)
		}
	}

	// One RFI shows up. Both retries list it before either stores it.
	e.fp.RFIs = []procore.RFI{{ID: 501, Number: "214", Subject: "Twisted column", CreatedBy: pat, CreatedAt: e.clock.Now().Format(time.RFC3339)}}
	var arrived atomic.Int32
	var bothListed sync.WaitGroup
	bothListed.Add(2)
	e.fp.OnListRFIs = func() {
		if arrived.Add(1) <= 2 {
			bothListed.Done()
			bothListed.Wait()
		}
	}
	errs := make(chan error, 2)
	for _, d := range []Draft{a, b} {
		go func() { errs <- e.drafts.RunSubmit(ctx, d.ID) }()
	}
	for range 2 {
		select {
		case <-errs:
		case <-time.After(15 * time.Second):
			t.Fatal("recoveries did not finish within 15s")
		}
	}

	owners := 0
	for _, d := range []Draft{a, b} {
		got := e.mustGet(t, d.ID)
		if got.Result != nil && got.Result.ProcoreID == 501 {
			owners++
			if got.Status != StatusSubmitted {
				t.Errorf("owner %s status = %q", d.ID, got.Status)
			}
		} else if got.Status == StatusSubmitted {
			t.Errorf("draft %s submitted with %+v", d.ID, got.Result)
		}
	}
	if owners != 1 {
		t.Fatalf("%d drafts adopted RFI 501, want exactly 1", owners)
	}
}
