package pipeline

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/monroeresidential/fieldassistant/api/internal/procore"
)

// riverQueue mimics River's unique insert: one job ID per draft.
type riverQueue struct {
	ids   map[uuid.UUID]int64
	calls int
}

func (q *riverQueue) enqueue(_ context.Context, id uuid.UUID) (int64, error) {
	q.calls++
	if q.ids == nil {
		q.ids = map[uuid.UUID]int64{}
	}
	if _, ok := q.ids[id]; !ok {
		q.ids[id] = int64(700 + len(q.ids))
	}
	return q.ids[id], nil
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
		if _, err := e.drafts.Submit(ctx, e.user, d.ID, qa.enqueue); err != nil {
			t.Errorf("Submit A: %v", err)
		}
		if err := e.drafts.RunSubmit(ctx, d.ID); err != nil {
			t.Errorf("RunSubmit A: %v", err)
		}
	}
	out, err := e.drafts.Submit(ctx, e.user, d.ID, qb.enqueue)
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
// storing its job gets the same job, and the draft posts once.
func TestSubmit_ConcurrentWhileEnqueueing_SameJob(t *testing.T) {
	e := newDraftsEnv(t)
	e.fp.Created = procore.RFICreated{ID: 501}
	d := e.submittable(t, twistedFinal)
	ctx := context.Background()
	q := &riverQueue{}
	var b SubmitOutcome
	nested := false
	enqueueA := func(ctx context.Context, id uuid.UUID) (int64, error) {
		if !nested {
			nested = true
			var err error
			if b, err = e.drafts.Submit(ctx, e.user, id, q.enqueue); err != nil {
				t.Errorf("Submit B: %v", err)
			}
		}
		return q.enqueue(ctx, id)
	}
	a, err := e.drafts.Submit(ctx, e.user, d.ID, enqueueA)
	if err != nil {
		t.Fatalf("Submit A: %v", err)
	}
	if a.JobID == 0 || b.JobID != a.JobID || a.AlreadySubmitted || b.AlreadySubmitted {
		t.Fatalf("A = %+v, B = %+v; want one shared job", a, b)
	}
	if err := e.drafts.RunSubmit(ctx, d.ID); err != nil {
		t.Fatalf("RunSubmit: %v", err)
	}
	if n := e.fp.CallCount("CreateRFI"); n != 1 {
		t.Errorf("CreateRFI called %d times, want 1", n)
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
	_, err := e.drafts.Submit(ctx, e.user, d.ID, q.enqueue)
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
	if _, err := e.drafts.Submit(ctx, e.user, d.ID, (&riverQueue{}).enqueue); !errors.Is(err, ErrDraftChanged) {
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
	if err := e.drafts.writeAttempt(ctx, e.mustGet(t, d.ID), 1, e.clock.Now()); err != nil {
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

func TestRunSubmit_LostResponse_Ambiguous_NoAdopt(t *testing.T) {
	e := newDraftsEnv(t)
	e.fp.Err["CreateRFI"] = fmt.Errorf("post rfi: %w", procore.ErrUnavailable)
	now := e.clock.Now().Format(time.RFC3339)
	e.fp.RFIs = []procore.RFI{
		{ID: 501, Number: "214", Subject: "Twisted column", CreatedBy: pat, CreatedAt: now},
		{ID: 502, Number: "215", Subject: "Twisted column", CreatedBy: pat, CreatedAt: now},
	}
	d := e.submittable(t, twistedFinal)
	e.submit(t, d)
	ctx := context.Background()

	if err := e.drafts.RunSubmit(ctx, d.ID); !errors.Is(err, procore.ErrUnavailable) {
		t.Fatalf("RunSubmit = %v, want ErrUnavailable for River to retry", err)
	}
	if got := e.mustGet(t, d.ID); got.Status != StatusSubmitting || got.Result != nil {
		t.Fatalf("draft = status %q result %+v", got.Status, got.Result)
	}

	// The retry finds the same two candidates before posting and neither
	// adopts one nor posts a third.
	delete(e.fp.Err, "CreateRFI")
	if err := e.drafts.RunSubmit(ctx, d.ID); err == nil {
		t.Fatal("retry = nil, want an error while the match is ambiguous")
	}
	if n := e.fp.CallCount("CreateRFI"); n != 1 {
		t.Errorf("CreateRFI called %d times, want 1", n)
	}
	if got := e.mustGet(t, d.ID); got.Status != StatusSubmitting {
		t.Errorf("status = %q, want submitting", got.Status)
	}
}
