package pipeline

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/monroeresidential/fieldassistant/api/internal/procore"
	"github.com/monroeresidential/fieldassistant/api/internal/store"
)

// riverQueue mimics River's unique insert by args: one live job per draft
// and attempt, and a new job once that job is finished or for a new
// attempt. It ignores the transaction.
type riverQueue struct {
	mu       sync.Mutex
	ids      map[jobArgs]int64
	finished map[int64]bool
	calls    int
	// onEnqueue, when set, runs at the start of EnqueueTx, outside the
	// lock; an error it returns fails the insert.
	onEnqueue func() error
}

type jobArgs struct {
	draft   uuid.UUID
	attempt int
}

func (q *riverQueue) EnqueueTx(_ context.Context, _ pgx.Tx, draft uuid.UUID, attempt int) (int64, error) {
	id := jobArgs{draft, attempt}
	q.mu.Lock()
	hook := q.onEnqueue
	q.mu.Unlock()
	if hook != nil {
		if err := hook(); err != nil {
			return 0, err
		}
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	q.calls++
	if q.ids == nil {
		q.ids = map[jobArgs]int64{}
	}
	if job, ok := q.ids[id]; ok && !q.finished[job] {
		return job, nil
	}
	q.ids[id] = int64(700 + q.calls)
	return q.ids[id], nil
}

func (q *riverQueue) JobFinished(_ context.Context, jobID int64) (bool, error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.finished[jobID], nil
}

func (q *riverQueue) finish(jobID int64) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.finished == nil {
		q.finished = map[int64]bool{}
	}
	q.finished[jobID] = true
}

// waitForRowLockWaiter waits until some session is blocked on a row lock,
// so a test knows a concurrent Submit has reached the draft row held by an
// open transaction. It gives up quietly after five seconds.
func waitForRowLockWaiter(t *testing.T, st *store.Store) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		var n int
		err := st.Pool.QueryRow(context.Background(),
			`select count(*) from pg_stat_activity where wait_event_type = 'Lock' and wait_event in ('transactionid', 'tuple')`).Scan(&n)
		if err != nil {
			t.Errorf("pg_stat_activity: %v", err)
			return
		}
		if n > 0 {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Log("no session waited on a row lock within 5s")
}

type submitResult struct {
	out SubmitOutcome
	err error
}

func awaitSubmit(t *testing.T, ch <-chan submitResult) submitResult {
	t.Helper()
	select {
	case r := <-ch:
		return r
	case <-time.After(15 * time.Second):
		t.Fatal("concurrent Submit did not finish within 15s")
		return submitResult{}
	}
}

// TestSubmit_Concurrent_PostsOnce is the reviewer's race: B validates, then
// A submits and its job posts before B moves the draft to submitting. B
// must see the submitted draft, not overwrite it and enqueue a second job.
func TestSubmit_Concurrent_PostsOnce(t *testing.T) {
	e := newDraftsEnv(t)
	e.fp.Created = procore.RFICreated{ID: 501, Number: "214"}
	d := e.submittable(t, twistedFinal)
	ctx := context.Background()
	qa, qb := &riverQueue{}, &riverQueue{}

	e.drafts.beforeMarkSubmitting = func() {
		e.drafts.beforeMarkSubmitting = nil // only B's first pass
		if _, err := e.drafts.Submit(ctx, e.user, d.ID, qa); err != nil {
			t.Errorf("Submit A: %v", err)
		}
		if err := e.drafts.RunSubmit(ctx, d.ID); err != nil {
			t.Errorf("RunSubmit A: %v", err)
		}
	}
	out, err := e.drafts.Submit(ctx, e.user, d.ID, qb)
	if err != nil {
		t.Fatalf("Submit B: %v", err)
	}
	if !out.AlreadySubmitted || out.Draft.Result == nil || out.Draft.Result.ProcoreID != 501 {
		t.Fatalf("Submit B = %+v, want AlreadySubmitted with A's result", out)
	}
	if qb.calls != 0 {
		t.Errorf("B enqueued %d jobs", qb.calls)
	}
	// Even a stray run of B's job could not post again.
	if err := e.drafts.RunSubmit(ctx, d.ID); err != nil {
		t.Fatalf("stray RunSubmit: %v", err)
	}
	if n := e.fp.CallCount("CreateRFI"); n != 1 {
		t.Errorf("CreateRFI called %d times, want 1", n)
	}
	if got := e.mustGet(t, d.ID); got.Status != StatusSubmitted || got.Result == nil || got.Result.ProcoreID != 501 {
		t.Errorf("draft = status %q result %+v", got.Status, got.Result)
	}
}

// A Submit that arrives while another is between marking the draft and
// storing its job waits for that transaction, then gets the same job, and
// the draft posts once.
func TestSubmit_ConcurrentWhileEnqueueing_SameJob(t *testing.T) {
	e := newDraftsEnv(t)
	e.fp.Created = procore.RFICreated{ID: 501}
	d := e.submittable(t, twistedFinal)
	ctx := context.Background()
	q := &riverQueue{}
	bDone := make(chan submitResult, 1)
	var started atomic.Bool
	q.onEnqueue = func() error {
		if started.Swap(true) {
			return nil
		}
		go func() {
			out, err := e.drafts.Submit(ctx, e.user, d.ID, q)
			bDone <- submitResult{out, err}
		}()
		waitForRowLockWaiter(t, e.st)
		return nil
	}
	a, err := e.drafts.Submit(ctx, e.user, d.ID, q)
	if err != nil {
		t.Fatalf("Submit A: %v", err)
	}
	b := awaitSubmit(t, bDone)
	if b.err != nil {
		t.Fatalf("Submit B: %v", b.err)
	}
	if a.JobID == 0 || b.out.JobID != a.JobID || a.AlreadySubmitted || b.out.AlreadySubmitted {
		t.Fatalf("A = %+v, B = %+v; want one shared job", a, b.out)
	}
	if err := e.drafts.RunSubmit(ctx, d.ID); err != nil {
		t.Fatalf("RunSubmit: %v", err)
	}
	if n := e.fp.CallCount("CreateRFI"); n != 1 {
		t.Errorf("CreateRFI called %d times, want 1", n)
	}
}

// A failed job insert leaves nothing behind: the draft is not submitting
// without a job, and a concurrent Submit that was waiting on the row goes on
// to submit it. The failed Submit's caller retrying afterwards sees the
// submitted draft, and nothing restores the draft over it.
func TestSubmit_EnqueueFailure_DoesNotOverwriteSubmitted(t *testing.T) {
	e := newDraftsEnv(t)
	e.fp.Created = procore.RFICreated{ID: 501, Number: "214"}
	d := e.submittable(t, twistedFinal)
	ctx := context.Background()
	q := &riverQueue{}
	boom := errors.New("job insert failed")
	bDone := make(chan submitResult, 1)
	var started atomic.Bool
	q.onEnqueue = func() error {
		if started.Swap(true) {
			return nil // B's insert succeeds
		}
		go func() {
			out, err := e.drafts.Submit(ctx, e.user, d.ID, q)
			if err == nil {
				err = e.drafts.RunSubmit(ctx, d.ID)
			}
			bDone <- submitResult{out, err}
		}()
		waitForRowLockWaiter(t, e.st)
		return boom
	}

	if _, err := e.drafts.Submit(ctx, e.user, d.ID, q); !errors.Is(err, boom) {
		t.Fatalf("Submit A = %v, want the insert error", err)
	}
	if b := awaitSubmit(t, bDone); b.err != nil {
		t.Fatalf("Submit and RunSubmit B: %v", b.err)
	}
	if got := e.mustGet(t, d.ID); got.Status != StatusSubmitted || got.Result == nil || got.Result.ProcoreID != 501 {
		t.Fatalf("draft = status %q result %+v, want submitted with 501", got.Status, got.Result)
	}

	// A's caller retries: the draft is already submitted.
	out, err := e.drafts.Submit(ctx, e.user, d.ID, q)
	if err != nil || !out.AlreadySubmitted {
		t.Fatalf("retry Submit = %+v, %v; want AlreadySubmitted", out, err)
	}
	if err := e.drafts.RunSubmit(ctx, d.ID); err != nil {
		t.Fatalf("stray RunSubmit: %v", err)
	}
	if n := e.fp.CallCount("CreateRFI"); n != 1 {
		t.Errorf("CreateRFI called %d times, want 1", n)
	}
	if got := e.mustGet(t, d.ID); got.Status != StatusSubmitted {
		t.Errorf("status = %q, want submitted", got.Status)
	}
}

// A submitting draft whose job died (discarded or cancelled) is not stuck:
// Submit queues a new job, which checks for the dead job's RFI before
// posting.
func TestSubmit_DeadJob_Reenqueues(t *testing.T) {
	e := newDraftsEnv(t)
	e.fp.Created = procore.RFICreated{ID: 999}
	d := e.submittable(t, twistedFinal)
	ctx := context.Background()
	q := &riverQueue{}
	first, err := e.drafts.Submit(ctx, e.user, d.ID, q)
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}
	// While the job is live, Submit hands it back.
	if again, err := e.drafts.Submit(ctx, e.user, d.ID, q); err != nil || again.JobID != first.JobID {
		t.Fatalf("Submit with live job = %+v, %v; want job %d", again, err, first.JobID)
	}

	// The job posted, then died before recording the outcome.
	if err := e.drafts.writeAttempt(ctx, e.mustGet(t, d.ID), 1, "Twisted column", e.clock.Now()); err != nil {
		t.Fatalf("writeAttempt: %v", err)
	}
	e.fp.RFIs = []procore.RFI{{ID: 501, Number: "214", Subject: "Twisted column", CreatedBy: pat, CreatedAt: e.clock.Now().Format(time.RFC3339)}}
	q.finish(first.JobID)

	out, err := e.drafts.Submit(ctx, e.user, d.ID, q)
	if err != nil {
		t.Fatalf("Submit after dead job: %v", err)
	}
	if out.JobID == 0 || out.JobID == first.JobID || out.AlreadySubmitted {
		t.Fatalf("Submit after dead job = %+v, want a new job", out)
	}
	if got := e.mustGet(t, d.ID); got.Status != StatusSubmitting || got.JobID != out.JobID {
		t.Fatalf("draft = status %q job %d, want submitting with job %d", got.Status, got.JobID, out.JobID)
	}
	if err := e.drafts.RunSubmit(ctx, d.ID); err != nil {
		t.Fatalf("RunSubmit: %v", err)
	}
	if n := e.fp.CallCount("CreateRFI"); n != 0 {
		t.Errorf("CreateRFI called %d times, want 0", n)
	}
	if got := e.mustGet(t, d.ID); got.Status != StatusSubmitted || got.Result == nil || got.Result.ProcoreID != 501 {
		t.Fatalf("draft = status %q result %+v, want submitted with 501", got.Status, got.Result)
	}
}

// An edit landing between validation and the transition is validated
// before anything is queued.
func TestSubmit_EditDuringSubmit_Revalidates(t *testing.T) {
	e := newDraftsEnv(t)
	d := e.submittable(t, twistedFinal)
	ctx := context.Background()
	q := &riverQueue{}
	e.drafts.beforeMarkSubmitting = func() {
		e.drafts.beforeMarkSubmitting = nil
		// Location 999 is not in the lookups, so the edit is flagged.
		edit := `{"subject":"Twisted column","question_body":"Is it OK?","assignee_ids":[11],"location_id":999}`
		if _, err := e.drafts.SaveEdits(ctx, e.user, d.ID, json.RawMessage(edit)); err != nil {
			t.Errorf("SaveEdits: %v", err)
		}
	}
	_, err := e.drafts.Submit(ctx, e.user, d.ID, q)
	var un *ErrUnresolved
	if !errors.As(err, &un) || !reflect.DeepEqual(un.Fields, []string{"location_id"}) {
		t.Fatalf("Submit = %v, want ErrUnresolved for location_id", err)
	}
	if got := e.mustGet(t, d.ID); got.Status != StatusReviewed {
		t.Errorf("status = %q, want reviewed", got.Status)
	}
	if q.calls != 0 {
		t.Errorf("enqueued %d jobs", q.calls)
	}
}

func TestSubmit_EditedOnEveryPass_ErrDraftChanged(t *testing.T) {
	e := newDraftsEnv(t)
	d := e.submittable(t, twistedFinal)
	ctx := context.Background()
	e.drafts.beforeMarkSubmitting = func() {
		edit := `{"subject":"Twisted column","question_body":"Is it OK?","assignee_ids":[11],"location_id":77}`
		if _, err := e.drafts.SaveEdits(ctx, e.user, d.ID, json.RawMessage(edit)); err != nil {
			t.Errorf("SaveEdits: %v", err)
		}
	}
	if _, err := e.drafts.Submit(ctx, e.user, d.ID, &riverQueue{}); !errors.Is(err, ErrDraftChanged) {
		t.Fatalf("Submit = %v, want ErrDraftChanged", err)
	}
	if got := e.mustGet(t, d.ID); got.Status != StatusReviewed {
		t.Errorf("status = %q, want reviewed", got.Status)
	}
}

func TestRunSubmit_MarkerWrittenBeforePost(t *testing.T) {
	e := newDraftsEnv(t)
	e.fp.Created = procore.RFICreated{ID: 501}
	d := e.submittable(t, twistedFinal)
	e.submit(t, d)
	var lastKind string
	var marker attemptAudit
	e.fp.OnCreateRFI = func() {
		evs := auditEvents(t, e.st, d.ID)
		last := evs[len(evs)-1]
		lastKind = last.Kind
		_ = json.Unmarshal(last.Payload, &marker)
	}
	if err := e.drafts.RunSubmit(context.Background(), d.ID); err != nil {
		t.Fatalf("RunSubmit: %v", err)
	}
	if lastKind != AuditAttempt || marker.Attempt != 1 || !marker.At.Equal(e.clock.Now()) {
		t.Fatalf("latest audit event at the POST = %q %+v, want attempt 1 at %v", lastKind, marker, e.clock.Now())
	}
}

// A crash between the marker and recording the outcome leaves only the
// marker; the next attempt finds the RFI instead of posting again.
func TestRunSubmit_AfterCrashMidPost_FindsRFIBeforePosting(t *testing.T) {
	e := newDraftsEnv(t)
	e.fp.Created = procore.RFICreated{ID: 999}
	d := e.submittable(t, twistedFinal)
	e.submit(t, d)
	ctx := context.Background()
	if err := e.drafts.writeAttempt(ctx, e.mustGet(t, d.ID), 1, "Twisted column", e.clock.Now()); err != nil {
		t.Fatalf("writeAttempt: %v", err)
	}
	e.fp.RFIs = []procore.RFI{{ID: 501, Number: "214", Subject: "Twisted column", CreatedBy: pat, CreatedAt: e.clock.Now().Add(time.Second).Format(time.RFC3339)}}
	e.clock.Advance(3 * time.Minute)

	if err := e.drafts.RunSubmit(ctx, d.ID); err != nil {
		t.Fatalf("RunSubmit: %v", err)
	}
	if n := e.fp.CallCount("CreateRFI"); n != 0 {
		t.Errorf("CreateRFI called %d times, want 0", n)
	}
	got := e.mustGet(t, d.ID)
	if got.Status != StatusSubmitted || got.Result == nil || got.Result.ProcoreID != 501 {
		t.Fatalf("draft = status %q result %+v", got.Status, got.Result)
	}
	ev := lastAudit(t, e, d.ID, AuditSubmitted)
	var resp struct {
		BeforePost bool `json:"before_post"`
	}
	if err := json.Unmarshal(ev.ProcoreResponse, &resp); err != nil || !resp.BeforePost {
		t.Errorf("submitted audit response = %s (%v)", ev.ProcoreResponse, err)
	}
}

// The job's context ending during the POST must not lose the created RFI.
func TestRunSubmit_JobContextDoneAfterPost_StillStoresResult(t *testing.T) {
	e := newDraftsEnv(t)
	e.fp.Created = procore.RFICreated{ID: 501, Number: "214"}
	d := e.submittable(t, twistedFinal)
	e.submit(t, d)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	e.fp.OnCreateRFI = cancel

	if err := e.drafts.RunSubmit(ctx, d.ID); err != nil {
		t.Fatalf("RunSubmit: %v", err)
	}
	if got := e.mustGet(t, d.ID); got.Status != StatusSubmitted || got.Result == nil || got.Result.ProcoreID != 501 {
		t.Fatalf("draft = status %q result %+v", got.Status, got.Result)
	}
}

func TestRunSubmit_LostResponse_CreatorMismatch_NoAdopt(t *testing.T) {
	e := newDraftsEnv(t)
	e.fp.Err["CreateRFI"] = fmt.Errorf("post rfi: %w", procore.ErrUnavailable)
	e.fp.RFIs = []procore.RFI{{
		ID: 501, Number: "214", Subject: "Twisted column",
		CreatedBy: procore.Person{ID: 7, Name: "Someone Else"}, CreatedAt: e.clock.Now().Format(time.RFC3339),
	}}
	d := e.submittable(t, twistedFinal)
	e.submit(t, d)

	if err := e.drafts.RunSubmit(context.Background(), d.ID); !errors.Is(err, procore.ErrUnavailable) {
		t.Fatalf("RunSubmit = %v, want ErrUnavailable for River to retry", err)
	}
	if got := e.mustGet(t, d.ID); got.Status != StatusSubmitting || got.Result != nil {
		t.Fatalf("draft = status %q result %+v", got.Status, got.Result)
	}
}

// assertAmbiguousFailure checks that d failed definitively because several
// RFIs could be its own.
func assertAmbiguousFailure(t *testing.T, e *draftsEnv, d Draft) {
	t.Helper()
	got := e.mustGet(t, d.ID)
	if got.Status != StatusFailed || got.Result != nil || got.Error == nil || got.Error.Code != CodeProcoreValidation {
		t.Fatalf("draft = status %q result %+v error %+v, want failed procore_validation", got.Status, got.Result, got.Error)
	}
	var form *Unresolved
	for i := range got.Unresolved {
		if got.Unresolved[i].Field == FormField {
			form = &got.Unresolved[i]
		}
	}
	if form == nil || form.Message != AmbiguousRFIMessage || form.Reason != ReasonProcoreValidation {
		t.Errorf("unresolved = %+v, want a _form entry with %q", got.Unresolved, AmbiguousRFIMessage)
	}
}

// Several RFIs that could be this draft's is not something a retry can
// settle: the draft fails at once and tells the user to check Procore, and
// the user's next submit posts.
func TestRunSubmit_Recovery_Ambiguous_FailsDefinitively(t *testing.T) {
	twoMatches := func(e *draftsEnv) []procore.RFI {
		now := e.clock.Now().Format(time.RFC3339)
		return []procore.RFI{
			{ID: 501, Number: "214", Subject: "Twisted column", CreatedBy: pat, CreatedAt: now},
			{ID: 502, Number: "215", Subject: "Twisted column", CreatedBy: pat, CreatedAt: now},
		}
	}

	t.Run("after a lost response", func(t *testing.T) {
		e := newDraftsEnv(t)
		e.fp.Err["CreateRFI"] = fmt.Errorf("post rfi: %w", procore.ErrUnavailable)
		e.fp.RFIs = twoMatches(e)
		d := e.submittable(t, twistedFinal)
		e.submit(t, d)
		ctx := context.Background()

		if err := e.drafts.RunSubmit(ctx, d.ID); err != nil {
			t.Fatalf("RunSubmit = %v, want nil (a definitive failure)", err)
		}
		assertAmbiguousFailure(t, e, d)

		// The user changes the subject and submits again: it posts once.
		delete(e.fp.Err, "CreateRFI")
		e.fp.Created = procore.RFICreated{ID: 777}
		if _, err := e.drafts.SaveEdits(ctx, e.user, d.ID, json.RawMessage(
			`{"subject":"Twisted column C4","question_body":"Is it OK?","assignee_ids":[11],"location_id":77,"rfi_manager_id":42,"draft":true}`)); err != nil {
			t.Fatalf("SaveEdits: %v", err)
		}
		e.submit(t, d)
		if err := e.drafts.RunSubmit(ctx, d.ID); err != nil {
			t.Fatalf("resubmit: %v", err)
		}
		if n := e.fp.CallCount("CreateRFI"); n != 2 {
			t.Errorf("CreateRFI called %d times, want 2", n)
		}
		if got := e.mustGet(t, d.ID); got.Status != StatusSubmitted || got.Result == nil || got.Result.ProcoreID != 777 {
			t.Errorf("draft = status %q result %+v", got.Status, got.Result)
		}
	})

	t.Run("before a retry posts", func(t *testing.T) {
		e := newDraftsEnv(t)
		e.fp.Err["CreateRFI"] = fmt.Errorf("post rfi: %w", procore.ErrUnavailable)
		d := e.submittable(t, twistedFinal)
		e.submit(t, d)
		ctx := context.Background()
		if err := e.drafts.RunSubmit(ctx, d.ID); !errors.Is(err, procore.ErrUnavailable) {
			t.Fatalf("first attempt = %v, want ErrUnavailable", err)
		}

		delete(e.fp.Err, "CreateRFI")
		e.fp.RFIs = twoMatches(e)
		if err := e.drafts.RunSubmit(ctx, d.ID); err != nil {
			t.Fatalf("retry = %v, want nil (a definitive failure)", err)
		}
		if n := e.fp.CallCount("CreateRFI"); n != 1 {
			t.Errorf("CreateRFI called %d times, want 1", n)
		}
		assertAmbiguousFailure(t, e, d)
	})
}

// A POST Procore refused created nothing, so the resubmit after fixing the
// fields posts without looking for that attempt's RFI. A later attempt with
// no recorded outcome makes the run unknown again, with only its own
// subject.
func TestRunSubmit_ResubmitAfter422_RejectedAttemptNotUnknown(t *testing.T) {
	e := newDraftsEnv(t)
	e.fp.Err["CreateRFI"] = &procore.ValidationError{Fields: map[string][]string{"due_date": {"can't be blank"}}}
	e.fp.Created = procore.RFICreated{ID: 777}
	d := e.submittable(t, twistedFinal)
	e.submit(t, d)
	ctx := context.Background()
	if err := e.drafts.RunSubmit(ctx, d.ID); err != nil {
		t.Fatalf("RunSubmit: %v", err)
	}
	h, err := e.drafts.priorAttempts(ctx, d.ID)
	if err != nil || h.unknown || h.attempts != 1 {
		t.Fatalf("history after a 422 = %+v, %v; want one attempt, not unknown", h, err)
	}

	// A later attempt that crashed mid-POST.
	if err := e.drafts.writeAttempt(ctx, e.mustGet(t, d.ID), 2, "New subject", e.clock.Now()); err != nil {
		t.Fatal(err)
	}
	h, err = e.drafts.priorAttempts(ctx, d.ID)
	if err != nil || !h.unknown || !reflect.DeepEqual(h.subjects, []string{"New subject"}) {
		t.Fatalf("history = %+v, %v; want unknown with only the later attempt's subject", h, err)
	}
}

// A token error before any POST is not an unknown outcome: the retry posts
// without reading the RFI list, and never adopts a same-subject RFI it did
// not create.
func TestRunSubmit_TokenErrorBeforePost_NotUnknown(t *testing.T) {
	e := newDraftsEnv(t)
	e.fp.Created = procore.RFICreated{ID: 999, Number: "300"}
	d := e.submittable(t, twistedFinal)
	e.submit(t, d)
	ctx := context.Background()

	e.tokens.setErr(e.user, fmt.Errorf("refresh: %w", procore.ErrUnavailable))
	if err := e.drafts.RunSubmit(ctx, d.ID); err == nil {
		t.Fatal("RunSubmit = nil, want the token error for River to retry")
	}
	e.tokens.setErr(e.user, nil)
	// Someone else's RFI (by the same Procore account) with this subject.
	e.fp.RFIs = []procore.RFI{{ID: 501, Number: "214", Subject: "Twisted column", CreatedBy: pat, CreatedAt: e.clock.Now().Format(time.RFC3339)}}

	if err := e.drafts.RunSubmit(ctx, d.ID); err != nil {
		t.Fatalf("retry: %v", err)
	}
	if n := e.fp.CallCount("CreateRFI"); n != 1 {
		t.Errorf("CreateRFI called %d times, want 1", n)
	}
	if n := e.fp.CallCount("ListRFIs"); n != 0 {
		t.Errorf("ListRFIs called %d times, want 0", n)
	}
	if got := e.mustGet(t, d.ID); got.Status != StatusSubmitted || got.Result == nil || got.Result.ProcoreID != 999 {
		t.Fatalf("draft = status %q result %+v", got.Status, got.Result)
	}
}

// An RFI another draft already stores is never adopted, after a lost
// response or before a retry.
func TestRunSubmit_Recovery_SkipsRFIOwnedByOtherDraft(t *testing.T) {
	e := newDraftsEnv(t)
	ctx := context.Background()
	e.fp.Created = procore.RFICreated{ID: 501, Number: "214"}
	a := e.submittable(t, twistedFinal)
	e.submit(t, a)
	if err := e.drafts.RunSubmit(ctx, a.ID); err != nil {
		t.Fatalf("RunSubmit A: %v", err)
	}
	now := e.clock.Now().Format(time.RFC3339)
	e.fp.RFIs = []procore.RFI{{ID: 501, Number: "214", Subject: "Twisted column", CreatedBy: pat, CreatedAt: now}}

	// B has the same subject and its POST's response is lost.
	e.fp.Err["CreateRFI"] = fmt.Errorf("post rfi: %w", procore.ErrUnavailable)
	b := e.submittable(t, twistedFinal)
	e.submit(t, b)
	if err := e.drafts.RunSubmit(ctx, b.ID); !errors.Is(err, procore.ErrUnavailable) {
		t.Fatalf("RunSubmit B = %v, want ErrUnavailable for River to retry", err)
	}
	if got := e.mustGet(t, b.ID); got.Status != StatusSubmitting || got.Result != nil {
		t.Fatalf("B after lost response = status %q result %+v, want submitting with no result", got.Status, got.Result)
	}

	// B's RFI shows up; the retry adopts it, not A's.
	delete(e.fp.Err, "CreateRFI")
	e.fp.RFIs = append(e.fp.RFIs, procore.RFI{ID: 502, Number: "215", Subject: "Twisted column", CreatedBy: pat, CreatedAt: now})
	if err := e.drafts.RunSubmit(ctx, b.ID); err != nil {
		t.Fatalf("retry B: %v", err)
	}
	if n := e.fp.CallCount("CreateRFI"); n != 2 {
		t.Errorf("CreateRFI called %d times, want 2 (A once, B once)", n)
	}
	if got := e.mustGet(t, b.ID); got.Status != StatusSubmitted || got.Result == nil || got.Result.ProcoreID != 502 {
		t.Fatalf("B = status %q result %+v, want submitted with 502", got.Status, got.Result)
	}
}

// The iOS client saves before every submit, so a resubmit after the retries
// ran out writes an edited event first. The save must not hide the earlier
// attempt: the resubmit finds its RFI instead of posting a second one.
func TestRunSubmit_ResubmitAfterSave_FindsRFIBeforePosting(t *testing.T) {
	e := newDraftsEnv(t)
	e.fp.Err["CreateRFI"] = fmt.Errorf("post rfi: %w", procore.ErrUnavailable)
	d := e.submittable(t, twistedFinal)
	e.submit(t, d)
	ctx := context.Background()
	err := e.drafts.RunSubmit(ctx, d.ID)
	if !errors.Is(err, procore.ErrUnavailable) {
		t.Fatalf("attempt = %v", err)
	}
	if err := e.drafts.FailSubmit(ctx, d.ID, err); err != nil {
		t.Fatalf("FailSubmit: %v", err)
	}

	// That attempt did create the RFI.
	delete(e.fp.Err, "CreateRFI")
	e.fp.Created = procore.RFICreated{ID: 999}
	e.fp.RFIs = []procore.RFI{{ID: 501, Number: "214", Subject: "Twisted column", CreatedBy: pat, CreatedAt: e.clock.Now().Format(time.RFC3339)}}
	e.clock.Advance(10 * time.Minute)
	cur := e.mustGet(t, d.ID)
	if _, err := e.drafts.SaveEdits(ctx, cur.UserID, d.ID, cur.Payload()); err != nil {
		t.Fatalf("SaveEdits: %v", err)
	}
	e.submit(t, d)
	if err := e.drafts.RunSubmit(ctx, d.ID); err != nil {
		t.Fatalf("resubmit: %v", err)
	}
	if n := e.fp.CallCount("CreateRFI"); n != 1 {
		t.Errorf("CreateRFI called %d times, want 1", n)
	}
	if got := e.mustGet(t, d.ID); got.Status != StatusSubmitted || got.Result == nil || got.Result.ProcoreID != 501 {
		t.Fatalf("draft = status %q result %+v", got.Status, got.Result)
	}
}
