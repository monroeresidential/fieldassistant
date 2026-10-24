package pipeline

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/monroeresidential/fieldassistant/api/internal/auth"
	"github.com/monroeresidential/fieldassistant/api/internal/llm/llmtest"
	"github.com/monroeresidential/fieldassistant/api/internal/procore"
	"github.com/monroeresidential/fieldassistant/api/internal/store/db"
)

// submittable creates a draft from final with location 77 in the lookups, so
// twistedFinal extracts with nothing unresolved.
func (e *draftsEnv) submittable(t *testing.T, final string) Draft {
	t.Helper()
	e.fp.Locations = []procore.Location{{ID: 77, Name: "Grid C4"}}
	e.llm.Steps = []llmtest.Step{{Final: final}}
	d, err := e.drafts.Create(context.Background(), e.input(uuid.New()))
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if len(d.Unresolved) != 0 {
		t.Fatalf("draft has unresolved fields: %+v", d.Unresolved)
	}
	return d
}

// pat is the draft user's Procore account in draftsEnv.
var pat = procore.Person{ID: 42, Name: "Pat Field"}

// enqueuer is a fake job queue that hands out job IDs and records drafts.
// Jobs in finished count as done; every other job is still live. A job it
// hands out exists even if the caller's transaction rolls back.
type enqueuer struct {
	next     int64
	drafts   []uuid.UUID
	finished map[int64]bool
}

func (q *enqueuer) EnqueueTx(_ context.Context, _ pgx.Tx, id uuid.UUID) (int64, error) {
	q.next++
	q.drafts = append(q.drafts, id)
	return q.next + 900, nil
}

func (q *enqueuer) JobFinished(_ context.Context, jobID int64) (bool, error) {
	return q.finished[jobID], nil
}

func (e *draftsEnv) mustGet(t *testing.T, id uuid.UUID) Draft {
	t.Helper()
	d, err := e.drafts.Get(context.Background(), e.user, id)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	return d
}

// submit runs Submit for d with a throwaway queue and fails on error.
func (e *draftsEnv) submit(t *testing.T, d Draft) {
	t.Helper()
	if _, err := e.drafts.Submit(context.Background(), e.user, d.ID, &enqueuer{}); err != nil {
		t.Fatalf("Submit: %v", err)
	}
}

// postedRFI returns the body of the last CreateRFI call, or nil.
func (e *draftsEnv) postedRFI() *procore.RFICreate {
	var posted *procore.RFICreate
	for _, c := range e.fp.Calls() {
		if c.Method == "CreateRFI" {
			posted = c.RFI
		}
	}
	return posted
}

// lastAudit returns the newest audit event of kind.
func lastAudit(t *testing.T, e *draftsEnv, id uuid.UUID, kind string) db.AuditEvent {
	t.Helper()
	evs := auditEvents(t, e.st, id)
	for i := len(evs) - 1; i >= 0; i-- {
		if evs[i].Kind == kind {
			return evs[i]
		}
	}
	t.Fatalf("no %s audit event in %d events", kind, len(evs))
	return db.AuditEvent{}
}

func TestRunSubmit_PostsAndStoresResult(t *testing.T) {
	e := newDraftsEnv(t)
	e.fp.Created = procore.RFICreated{ID: 501, Number: "214", Status: "draft", Link: "https://app.procore.com/rfis/501"}
	d := e.submittable(t, twistedFinal)
	q := &enqueuer{}
	ctx := context.Background()

	out, err := e.drafts.Submit(ctx, e.user, d.ID, q)
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}
	if out.AlreadySubmitted || out.JobID != 901 || out.Draft.Status != StatusSubmitting {
		t.Fatalf("outcome = %+v", out)
	}
	if got := e.mustGet(t, d.ID); got.Status != StatusSubmitting || got.JobID != 901 {
		t.Fatalf("after Submit: status %q job %d", got.Status, got.JobID)
	}

	if err := e.drafts.RunSubmit(ctx, d.ID); err != nil {
		t.Fatalf("RunSubmit: %v", err)
	}
	got := e.mustGet(t, d.ID)
	if got.Status != StatusSubmitted || got.Result == nil || got.Error != nil {
		t.Fatalf("draft = status %q result %+v error %+v", got.Status, got.Result, got.Error)
	}
	if *got.Result != (Result{ProcoreID: 501, Number: "214", URL: "https://app.procore.com/rfis/501"}) {
		t.Errorf("result = %+v", *got.Result)
	}

	for _, c := range e.fp.Calls() {
		if c.Method == "CreateRFI" && (c.AccessToken != "tok" || c.CompanyID != testCompany || c.ProjectID != testProject) {
			t.Errorf("CreateRFI call = %+v", c)
		}
	}
	posted := e.postedRFI()
	if posted == nil {
		t.Fatal("CreateRFI not called")
	}
	if posted.Subject != "Twisted column" || posted.Question.Body != "Is it OK?" ||
		!reflect.DeepEqual(posted.AssigneeIDs, []int64{11}) || posted.LocationID == nil || *posted.LocationID != 77 ||
		posted.RFIManagerID == nil || *posted.RFIManagerID != 42 || posted.Draft == nil || !*posted.Draft {
		t.Errorf("posted = %+v", posted)
	}

	ev := lastAudit(t, e, d.ID, AuditSubmitted)
	var req struct {
		RFI struct {
			Subject string `json:"subject"`
		} `json:"rfi"`
	}
	if err := json.Unmarshal(ev.ProcoreRequest, &req); err != nil || req.RFI.Subject != "Twisted column" {
		t.Errorf("procore_request = %s (%v)", ev.ProcoreRequest, err)
	}
	var resp procore.RFICreated
	if err := json.Unmarshal(ev.ProcoreResponse, &resp); err != nil || resp.ID != 501 {
		t.Errorf("procore_response = %s (%v)", ev.ProcoreResponse, err)
	}
}

func TestRunSubmit_BuildsImpactsAndDueDate(t *testing.T) {
	e := newDraftsEnv(t)
	e.fp.Created = procore.RFICreated{ID: 1}
	due := e.clock.Now().AddDate(0, 0, 3).Format("2006-01-02")
	final := fmt.Sprintf(`{"payload":{"subject":"S","question_body":"Q","assignee_ids":[],"due_date":%q,"schedule_impact":{"status":"yes_known","value":"5"}},"unresolved":[],"confidence":{}}`, due)
	d := e.submittable(t, final)
	e.submit(t, d)
	if err := e.drafts.RunSubmit(context.Background(), d.ID); err != nil {
		t.Fatalf("RunSubmit: %v", err)
	}
	posted := e.postedRFI()
	if posted == nil || posted.DueDate == nil || *posted.DueDate != due {
		t.Fatalf("due_date not posted: %+v", posted)
	}
	if posted.ScheduleImpact == nil || posted.ScheduleImpact.Status != "yes_known" || posted.ScheduleImpact.Value == nil || *posted.ScheduleImpact.Value != 5 {
		t.Errorf("schedule_impact = %+v", posted.ScheduleImpact)
	}
	if posted.CostImpact != nil {
		t.Errorf("cost_impact = %+v, want omitted", posted.CostImpact)
	}
}

func TestRunSubmit_ImpactValueNotNumber_FailsWithoutPosting(t *testing.T) {
	e := newDraftsEnv(t)
	final := `{"payload":{"subject":"S","question_body":"Q","assignee_ids":[],"schedule_impact":{"status":"yes_known","value":"two weeks"}},"unresolved":[],"confidence":{}}`
	d := e.submittable(t, final)
	e.submit(t, d)
	if err := e.drafts.RunSubmit(context.Background(), d.ID); err != nil {
		t.Fatalf("RunSubmit: %v", err)
	}
	if n := e.fp.CallCount("CreateRFI"); n != 0 {
		t.Fatalf("CreateRFI called %d times", n)
	}
	got := e.mustGet(t, d.ID)
	if got.Status != StatusFailed || got.Error == nil || got.Error.Code != "procore_validation" {
		t.Fatalf("draft = status %q error %+v", got.Status, got.Error)
	}
	if len(got.Unresolved) != 1 || got.Unresolved[0].Field != "schedule_impact" ||
		got.Unresolved[0].Reason != ReasonProcoreValidation || got.Unresolved[0].Message != "value must be a number" {
		t.Errorf("unresolved = %+v", got.Unresolved)
	}
}

func TestSubmit_Twice_PostsOnce(t *testing.T) {
	e := newDraftsEnv(t)
	e.fp.Created = procore.RFICreated{ID: 501, Number: "214"}
	d := e.submittable(t, twistedFinal)
	q := &enqueuer{}
	ctx := context.Background()

	if _, err := e.drafts.Submit(ctx, e.user, d.ID, q); err != nil {
		t.Fatalf("first Submit: %v", err)
	}
	// A second tap while the job is queued returns the same job.
	out, err := e.drafts.Submit(ctx, e.user, d.ID, q)
	if err != nil || out.JobID != 901 || out.AlreadySubmitted {
		t.Fatalf("Submit while submitting = %+v, %v", out, err)
	}
	if err := e.drafts.RunSubmit(ctx, d.ID); err != nil {
		t.Fatalf("RunSubmit: %v", err)
	}
	// A retried job finds the draft submitted and does not post.
	if err := e.drafts.RunSubmit(ctx, d.ID); err != nil {
		t.Fatalf("second RunSubmit: %v", err)
	}
	out, err = e.drafts.Submit(ctx, e.user, d.ID, q)
	if err != nil {
		t.Fatalf("third Submit: %v", err)
	}
	if !out.AlreadySubmitted || out.Draft.Result == nil || out.Draft.Result.ProcoreID != 501 {
		t.Fatalf("outcome = %+v", out)
	}
	if n := e.fp.CallCount("CreateRFI"); n != 1 {
		t.Errorf("CreateRFI called %d times, want 1", n)
	}
	if len(q.drafts) != 1 {
		t.Errorf("enqueued %d times, want 1", len(q.drafts))
	}
}

func TestRunSubmit_422_FlagsFields(t *testing.T) {
	e := newDraftsEnv(t)
	e.fp.Err["CreateRFI"] = &procore.ValidationError{Fields: map[string][]string{
		"due_date":      {"can't be blank"},
		"question.body": {"too short"},
		"rfi.base":      {"Something else", "and more"},
	}}
	d := e.submittable(t, twistedFinal)
	e.submit(t, d)

	if err := e.drafts.RunSubmit(context.Background(), d.ID); err != nil {
		t.Fatalf("RunSubmit = %v, want nil so River does not retry", err)
	}
	if n := e.fp.CallCount("CreateRFI"); n != 1 {
		t.Errorf("CreateRFI called %d times", n)
	}
	if n := e.fp.CallCount("ListRFIs"); n != 0 {
		t.Errorf("ListRFIs called %d times after a 422", n)
	}
	got := e.mustGet(t, d.ID)
	if got.Status != StatusFailed || got.Error == nil || got.Error.Code != "procore_validation" {
		t.Fatalf("draft = status %q error %+v", got.Status, got.Error)
	}
	byField := map[string]Unresolved{}
	for _, u := range got.Unresolved {
		byField[u.Field] = u
		if u.Reason != ReasonProcoreValidation {
			t.Errorf("%s reason = %q", u.Field, u.Reason)
		}
	}
	if byField["due_date"].Message != "can't be blank" {
		t.Errorf("due_date = %+v", byField["due_date"])
	}
	if byField["question_body"].Message != "too short" {
		t.Errorf("question_body = %+v", byField["question_body"])
	}
	if m := byField["_form"].Message; !strings.Contains(m, "Something else") || !strings.Contains(m, "and more") {
		t.Errorf("_form = %+v", byField["_form"])
	}
	if len(got.Unresolved) != 3 {
		t.Errorf("unresolved = %+v", got.Unresolved)
	}
	ev := lastAudit(t, e, d.ID, AuditFailed)
	if !strings.Contains(string(ev.ProcoreResponse), "too short") {
		t.Errorf("failed audit response = %s", ev.ProcoreResponse)
	}
}

// A 4xx that is not a parseable 422 (a 400, or a 422 in another shape) is
// still a refusal: the draft fails at once with Procore's message on _form,
// is not retried, and the RFI list is not read.
func TestRunSubmit_400_FailsDefinitively(t *testing.T) {
	long := strings.Repeat("x", 300)
	for _, tc := range []struct {
		name   string
		status int
		body   string
		want   string
	}{
		{"400", 400, `{"error":"param is missing: rfi"}`, `Procore refused the RFI (HTTP 400): {"error":"param is missing: rfi"}`},
		{"422 list", 422, `{"errors":["Subject is too long"]}`, `Procore refused the RFI (HTTP 422): {"errors":["Subject is too long"]}`},
		{"404 empty", 404, "", "Procore refused the RFI (HTTP 404)."},
		{"long body", 400, long, "Procore refused the RFI (HTTP 400): " + long[:200] + "…"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := newDraftsEnv(t)
			e.fp.Err["CreateRFI"] = fmt.Errorf("post rfi: %w", &procore.APIError{Status: tc.status, Body: tc.body})
			d := e.submittable(t, twistedFinal)
			e.submit(t, d)

			if err := e.drafts.RunSubmit(context.Background(), d.ID); err != nil {
				t.Fatalf("RunSubmit = %v, want nil (not retried)", err)
			}
			got := e.mustGet(t, d.ID)
			if got.Status != StatusFailed || got.Error == nil || got.Error.Code != CodeProcoreValidation {
				t.Fatalf("draft = status %q error %+v", got.Status, got.Error)
			}
			if len(got.Unresolved) != 1 || got.Unresolved[0].Field != FormField || got.Unresolved[0].Message != tc.want {
				t.Errorf("unresolved = %+v, want _form %q", got.Unresolved, tc.want)
			}
			if n := e.fp.CallCount("ListRFIs"); n != 0 {
				t.Errorf("ListRFIs called %d times, want 0", n)
			}
			ev := lastAudit(t, e, d.ID, AuditFailed)
			if tc.body != "" && !strings.Contains(string(ev.ProcoreResponse), strings.ReplaceAll(tc.body, `"`, `\"`)) {
				t.Errorf("failed audit response %s lacks the raw body", ev.ProcoreResponse)
			}
		})
	}

	// 408 and 429 may succeed later: they stay unknown and are retried.
	for _, status := range []int{408, 429} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			e := newDraftsEnv(t)
			e.fp.Err["CreateRFI"] = &procore.APIError{Status: status}
			d := e.submittable(t, twistedFinal)
			e.submit(t, d)
			if err := e.drafts.RunSubmit(context.Background(), d.ID); err == nil {
				t.Error("RunSubmit = nil, want an error for River to retry")
			}
			if got := e.mustGet(t, d.ID); got.Status != StatusSubmitting {
				t.Errorf("status = %q, want submitting", got.Status)
			}
		})
	}
}

func TestRunSubmit_LostResponse_FindsExistingRFI(t *testing.T) {
	e := newDraftsEnv(t)
	e.fp.Err["CreateRFI"] = fmt.Errorf("post rfi: %w", procore.ErrUnavailable)
	now := e.clock.Now()
	e.fp.RFIs = []procore.RFI{
		{ID: 400, Number: "100", Subject: "Twisted column", CreatedBy: pat, CreatedAt: now.Add(-48 * time.Hour).Format(time.RFC3339)},
		{ID: 501, Number: "214", Subject: "Twisted column", CreatedBy: pat, CreatedAt: now.Add(2 * time.Second).Format(time.RFC3339)},
		{ID: 502, Number: "215", Subject: "Something else", CreatedAt: now.Format(time.RFC3339)},
	}
	d := e.submittable(t, twistedFinal)
	e.submit(t, d)

	if err := e.drafts.RunSubmit(context.Background(), d.ID); err != nil {
		t.Fatalf("RunSubmit: %v", err)
	}
	got := e.mustGet(t, d.ID)
	if got.Status != StatusSubmitted || got.Result == nil || got.Result.ProcoreID != 501 || got.Result.Number != "214" {
		t.Fatalf("draft = status %q result %+v", got.Status, got.Result)
	}
	if n := e.fp.CallCount("CreateRFI"); n != 1 {
		t.Errorf("CreateRFI called %d times, want 1", n)
	}
	ev := lastAudit(t, e, d.ID, AuditSubmitted)
	var resp struct {
		Recovered bool `json:"recovered"`
	}
	if err := json.Unmarshal(ev.ProcoreResponse, &resp); err != nil || !resp.Recovered {
		t.Errorf("procore_response = %s (%v)", ev.ProcoreResponse, err)
	}
}

func TestRunSubmit_LostResponse_NoMatch_ReturnsErrorForRetry(t *testing.T) {
	e := newDraftsEnv(t)
	e.fp.Err["CreateRFI"] = fmt.Errorf("post rfi: %w", procore.ErrUnavailable)
	now := e.clock.Now()
	e.fp.RFIs = []procore.RFI{
		// Same subject but created well before this attempt: an older RFI.
		{ID: 400, Number: "100", Subject: "Twisted column", CreatedBy: pat, CreatedAt: now.Add(-5 * time.Minute).Format(time.RFC3339)},
		{ID: 502, Number: "215", Subject: "Twisted column at C5", CreatedAt: now.Format(time.RFC3339)},
	}
	d := e.submittable(t, twistedFinal)
	e.submit(t, d)

	err := e.drafts.RunSubmit(context.Background(), d.ID)
	if !errors.Is(err, procore.ErrUnavailable) {
		t.Fatalf("RunSubmit = %v, want ErrUnavailable for River to retry", err)
	}
	got := e.mustGet(t, d.ID)
	if got.Status != StatusSubmitting || got.Result != nil {
		t.Fatalf("draft = status %q result %+v", got.Status, got.Result)
	}
	ev := lastAudit(t, e, d.ID, AuditFailed)
	if !strings.Contains(string(ev.ProcoreResponse), "unavailable") {
		t.Errorf("failed audit response = %s", ev.ProcoreResponse)
	}
}

func TestRunSubmit_RetryAfterUnknownOutcome_FindsRFIBeforePosting(t *testing.T) {
	e := newDraftsEnv(t)
	e.fp.Err["CreateRFI"] = fmt.Errorf("post rfi: %w", procore.ErrUnavailable)
	d := e.submittable(t, twistedFinal)
	e.submit(t, d)
	ctx := context.Background()
	if err := e.drafts.RunSubmit(ctx, d.ID); !errors.Is(err, procore.ErrUnavailable) {
		t.Fatalf("first attempt = %v", err)
	}

	// The first POST did create the RFI; it only shows up in the list now.
	// The retry must find it instead of posting again.
	delete(e.fp.Err, "CreateRFI")
	e.fp.Created = procore.RFICreated{ID: 999}
	e.fp.RFIs = []procore.RFI{{ID: 501, Number: "214", Subject: "Twisted column", CreatedBy: pat, CreatedAt: e.clock.Now().Format(time.RFC3339)}}
	e.clock.Advance(2 * time.Minute)

	if err := e.drafts.RunSubmit(ctx, d.ID); err != nil {
		t.Fatalf("retry: %v", err)
	}
	if n := e.fp.CallCount("CreateRFI"); n != 1 {
		t.Errorf("CreateRFI called %d times, want 1", n)
	}
	got := e.mustGet(t, d.ID)
	if got.Status != StatusSubmitted || got.Result == nil || got.Result.ProcoreID != 501 {
		t.Fatalf("draft = status %q result %+v", got.Status, got.Result)
	}
}

func TestRunSubmit_ResubmitAfterRetriesRanOut_FindsRFIBeforePosting(t *testing.T) {
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

	delete(e.fp.Err, "CreateRFI")
	e.fp.Created = procore.RFICreated{ID: 999}
	e.fp.RFIs = []procore.RFI{{ID: 501, Number: "214", Subject: "Twisted column", CreatedBy: pat, CreatedAt: e.clock.Now().Format(time.RFC3339)}}
	e.clock.Advance(10 * time.Minute)
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

func TestRunSubmit_RetryAfterUnknownOutcome_PostsWhenNothingFound(t *testing.T) {
	e := newDraftsEnv(t)
	e.fp.Err["CreateRFI"] = fmt.Errorf("post rfi: %w", procore.ErrUnavailable)
	d := e.submittable(t, twistedFinal)
	e.submit(t, d)
	ctx := context.Background()
	if err := e.drafts.RunSubmit(ctx, d.ID); !errors.Is(err, procore.ErrUnavailable) {
		t.Fatalf("first attempt = %v", err)
	}
	delete(e.fp.Err, "CreateRFI")
	e.fp.Created = procore.RFICreated{ID: 777, Number: "300"}

	if err := e.drafts.RunSubmit(ctx, d.ID); err != nil {
		t.Fatalf("retry: %v", err)
	}
	if n := e.fp.CallCount("CreateRFI"); n != 2 {
		t.Errorf("CreateRFI called %d times, want 2", n)
	}
	if got := e.mustGet(t, d.ID); got.Status != StatusSubmitted || got.Result == nil || got.Result.ProcoreID != 777 {
		t.Fatalf("draft = status %q result %+v", got.Status, got.Result)
	}
}

func TestSubmit_UnresolvedPayload_Rejected(t *testing.T) {
	e := newDraftsEnv(t)
	e.fp.Locations = []procore.Location{{ID: 77, Name: "Grid C4"}}
	e.llm.Steps = []llmtest.Step{{Final: `{"payload":{"subject":"S","question_body":"Q","assignee_ids":[],"location_id":77},"unresolved":[{"field":"assignee_ids","reason":"ambiguous","candidates":[{"id":11,"label":"Ben"}]}],"confidence":{}}`}}
	ctx := context.Background()
	d, err := e.drafts.Create(ctx, e.input(uuid.New()))
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	q := &enqueuer{}

	_, err = e.drafts.Submit(ctx, e.user, d.ID, q)
	var un *ErrUnresolved
	if !errors.As(err, &un) {
		t.Fatalf("Submit = %v, want ErrUnresolved", err)
	}
	if !reflect.DeepEqual(un.Fields, []string{"assignee_ids"}) {
		t.Errorf("fields = %v", un.Fields)
	}
	if got := e.mustGet(t, d.ID); got.Status != StatusExtracted {
		t.Errorf("status = %q, want unchanged", got.Status)
	}
	if len(q.drafts) != 0 {
		t.Error("enqueued despite unresolved fields")
	}
}

func TestSubmit_OtherUser_NotFound(t *testing.T) {
	e := newDraftsEnv(t)
	d := e.submittable(t, twistedFinal)
	other := newDBUser(t, e.st, 99, "Other Person")
	if _, err := e.drafts.Submit(context.Background(), other, d.ID, &enqueuer{}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Submit = %v, want ErrNotFound", err)
	}
}

func TestRunSubmit_Reauth_MarksFailed(t *testing.T) {
	e := newDraftsEnv(t)
	d := e.submittable(t, twistedFinal)
	e.submit(t, d)
	e.tokens.setErr(e.user, fmt.Errorf("refresh: %w", auth.ErrReauthRequired))

	if err := e.drafts.RunSubmit(context.Background(), d.ID); err != nil {
		t.Fatalf("RunSubmit = %v, want nil", err)
	}
	if n := e.fp.CallCount("CreateRFI"); n != 0 {
		t.Errorf("CreateRFI called %d times", n)
	}
	got := e.mustGet(t, d.ID)
	if got.Status != StatusFailed || got.Error == nil || got.Error.Code != "reauth_required" {
		t.Fatalf("draft = status %q error %+v", got.Status, got.Error)
	}
	lastAudit(t, e, d.ID, AuditFailed)
}

func TestRunSubmit_PermissionDenied_MarksFailed(t *testing.T) {
	e := newDraftsEnv(t)
	e.fp.Err["CreateRFI"] = procore.ErrPermissionDenied
	d := e.submittable(t, twistedFinal)
	e.submit(t, d)
	if err := e.drafts.RunSubmit(context.Background(), d.ID); err != nil {
		t.Fatalf("RunSubmit = %v, want nil", err)
	}
	got := e.mustGet(t, d.ID)
	if got.Status != StatusFailed || got.Error == nil || got.Error.Code != "procore_permission_denied" {
		t.Fatalf("draft = status %q error %+v", got.Status, got.Error)
	}
}

func TestFailSubmit_MarksSubmittingDraftUnavailable(t *testing.T) {
	e := newDraftsEnv(t)
	d := e.submittable(t, twistedFinal)
	e.submit(t, d)
	if err := e.drafts.FailSubmit(context.Background(), d.ID, errors.New("procore unavailable: 503")); err != nil {
		t.Fatalf("FailSubmit: %v", err)
	}
	got := e.mustGet(t, d.ID)
	if got.Status != StatusFailed || got.Error == nil || got.Error.Code != "procore_unavailable" ||
		got.Error.Message != "Procore could not be reached. Try submitting again." {
		t.Fatalf("draft = status %q error %+v", got.Status, got.Error)
	}
	// The cause is audited, not shown to the user.
	if ev := lastAudit(t, e, d.ID, AuditFailed); !strings.Contains(string(ev.ProcoreResponse), "503") {
		t.Errorf("failed audit response = %s, want the cause", ev.ProcoreResponse)
	}
}

func TestFailSubmit_LeavesSubmittedDraft(t *testing.T) {
	e := newDraftsEnv(t)
	e.fp.Created = procore.RFICreated{ID: 501}
	d := e.submittable(t, twistedFinal)
	e.submit(t, d)
	ctx := context.Background()
	if err := e.drafts.RunSubmit(ctx, d.ID); err != nil {
		t.Fatalf("RunSubmit: %v", err)
	}
	if err := e.drafts.FailSubmit(ctx, d.ID, errors.New("late")); err != nil {
		t.Fatalf("FailSubmit: %v", err)
	}
	if got := e.mustGet(t, d.ID); got.Status != StatusSubmitted {
		t.Fatalf("status = %q, want submitted", got.Status)
	}
}

func TestSaveEdits_LockedWhileSubmitting_OpenAfterFailure(t *testing.T) {
	e := newDraftsEnv(t)
	e.fp.Err["CreateRFI"] = &procore.ValidationError{Fields: map[string][]string{"question.body": {"too short"}}}
	d := e.submittable(t, twistedFinal)
	e.submit(t, d)
	ctx := context.Background()
	if _, err := e.drafts.SaveEdits(ctx, e.user, d.ID, json.RawMessage(`{"subject":"S"}`)); !errors.Is(err, ErrDraftLocked) {
		t.Fatalf("SaveEdits while submitting = %v, want ErrDraftLocked", err)
	}
	if err := e.drafts.RunSubmit(ctx, d.ID); err != nil {
		t.Fatalf("RunSubmit: %v", err)
	}
	got, err := e.drafts.SaveEdits(ctx, e.user, d.ID, json.RawMessage(`{"subject":"S","question_body":"A longer question body","assignee_ids":[11],"location_id":77}`))
	if err != nil {
		t.Fatalf("SaveEdits after failure: %v", err)
	}
	if got.Status != StatusReviewed || got.Error != nil {
		t.Errorf("draft = status %q error %+v", got.Status, got.Error)
	}
}
