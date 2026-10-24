package pipeline

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/monroeresidential/fieldassistant/api/internal/auth"
	"github.com/monroeresidential/fieldassistant/api/internal/procore"
	"github.com/monroeresidential/fieldassistant/api/internal/store/db"
)

// ProcoreRFIs is the slice of the Procore client that submit uses.
// *procore.Client satisfies it.
type ProcoreRFIs interface {
	CreateRFI(ctx context.Context, accessToken string, companyID, projectID int64, body procore.RFICreate) (procore.RFICreated, error)
	ListRFIs(ctx context.Context, accessToken string, companyID, projectID int64) ([]procore.RFI, error)
}

var _ ProcoreRFIs = (*procore.Client)(nil)

// Draft error codes a failed submit stores, from the contract's error code set
// plus procore_validation for a rejected payload.
const (
	CodeProcoreValidation  = "procore_validation"
	CodeReauthRequired     = "reauth_required"
	CodePermissionDenied   = "procore_permission_denied"
	CodeProcoreUnavailable = "procore_unavailable"
	CodeInternal           = "internal"
)

const (
	// FormField holds Procore validation messages that name no payload field.
	FormField = "_form"
	// recoveryWindow is how far before an attempt's start an RFI with the
	// same subject may have been created and still count as that attempt's
	// record, to absorb clock skew between this server and Procore.
	recoveryWindow = time.Minute
)

// ErrUnresolved means the draft still has fields the reviewer must settle.
type ErrUnresolved struct {
	Fields []string
}

func (e *ErrUnresolved) Error() string {
	return "pipeline: draft has unresolved fields: " + strings.Join(e.Fields, ", ")
}

// SubmitOutcome is the result of Submit: the draft after the call and the job
// that posts it, or AlreadySubmitted when Procore already has the record.
type SubmitOutcome struct {
	JobID            int64
	Draft            Draft
	AlreadySubmitted bool
}

// Submit queues the user's draft for posting to Procore. A submitted draft
// returns AlreadySubmitted; a submitting draft returns its job. Otherwise the
// payload is validated against freshly warmed lookups, and any unresolved
// field, including an earlier flag on a field that is still empty, fails
// with *ErrUnresolved and leaves the draft as it was. A valid draft becomes
// submitting, enqueue is called and the job ID stored.
//
// enqueue must be idempotent per draft (the River queue dedupes by draft ID),
// since two concurrent calls may both reach it.
func (d *Drafts) Submit(ctx context.Context, userID, id uuid.UUID, enqueue func(ctx context.Context, draftID uuid.UUID) (int64, error)) (SubmitOutcome, error) {
	cur, err := d.Get(ctx, userID, id)
	if err != nil {
		return SubmitOutcome{}, err
	}
	switch {
	case cur.Status == StatusSubmitted:
		return SubmitOutcome{JobID: cur.JobID, Draft: cur, AlreadySubmitted: true}, nil
	case cur.Status == StatusSubmitting && cur.JobID != 0:
		return SubmitOutcome{JobID: cur.JobID, Draft: cur}, nil
	}

	var payload map[string]any
	if raw := cur.Payload(); len(raw) > 0 {
		if err := json.Unmarshal(raw, &payload); err != nil {
			return SubmitOutcome{}, fmt.Errorf("pipeline: decode payload: %w", err)
		}
	}
	_, unresolved, err := d.revalidate(ctx, userID, cur, payload)
	if err != nil {
		return SubmitOutcome{}, err
	}
	if len(unresolved) > 0 {
		fields := make([]string, 0, len(unresolved))
		for _, u := range unresolved {
			fields = append(fields, u.Field)
		}
		return SubmitOutcome{}, &ErrUnresolved{Fields: fields}
	}

	pid := pgtype.UUID{Bytes: id, Valid: true}
	// Clear an earlier submission's job so a concurrent Submit never
	// returns it for this one.
	err = d.st.WithTx(ctx, func(q *db.Queries) error {
		if err := q.SetDraftJob(ctx, db.SetDraftJobParams{ID: pid}); err != nil {
			return err
		}
		return q.SetDraftStatus(ctx, db.SetDraftStatusParams{ID: pid, Status: StatusSubmitting})
	})
	if err != nil {
		return SubmitOutcome{}, fmt.Errorf("pipeline: mark submitting: %w", err)
	}
	jobID, err := enqueue(ctx, id)
	if err != nil {
		// Put the draft back so the user can edit or retry.
		if rerr := d.st.Q.SetDraftStatus(ctx, db.SetDraftStatusParams{ID: pid, Status: cur.Status}); rerr != nil {
			d.log.Error("restore draft status after enqueue failure", "draft_id", id, "error", rerr)
		}
		return SubmitOutcome{}, fmt.Errorf("pipeline: enqueue submit: %w", err)
	}
	if err := d.st.Q.SetDraftJob(ctx, db.SetDraftJobParams{ID: pid, JobID: pgtype.Int8{Int64: jobID, Valid: true}}); err != nil {
		return SubmitOutcome{}, fmt.Errorf("pipeline: store job id: %w", err)
	}
	after, err := d.load(ctx, id)
	if err != nil {
		return SubmitOutcome{}, err
	}
	return SubmitOutcome{JobID: jobID, Draft: after}, nil
}

// revalidate runs Validate on payload against freshly warmed lookups, then
// again with cur's earlier unresolved entries for fields still empty, so a
// flag only the model or a Procore 422 raised is kept until the field is
// filled. Validate's own entries come first and win.
func (d *Drafts) revalidate(ctx context.Context, userID uuid.UUID, cur Draft, payload map[string]any) (map[string]any, []Unresolved, error) {
	f, ok := d.features[cur.Feature]
	if !ok {
		return nil, nil, fmt.Errorf("pipeline: unknown feature %q", cur.Feature)
	}
	sets, err := d.lookups.Warm(ctx, userID, cur.CompanyID, cur.ProjectID, f.LookupKinds())
	if err != nil {
		return nil, nil, err
	}
	project, err := d.lookups.Project(ctx, userID, cur.CompanyID, cur.ProjectID)
	if err != nil {
		return nil, nil, err
	}
	today, loc := d.now(), RailsZone(project.TimeZone)
	validated, unresolved := Validate(f, payload, nil, sets, today, loc)
	for _, u := range cur.Unresolved {
		if isEmptyValue(validated[u.Field]) {
			unresolved = append(unresolved, u)
		}
	}
	validated, unresolved = Validate(f, validated, unresolved, sets, today, loc)
	return validated, unresolved, nil
}

// RunSubmit is the submit job body: it posts a submitting draft to Procore
// as the draft's user and records the outcome. It returns an error only when
// the outcome is unknown and a retry may succeed; River retries the job then.
//
//   - A submitted draft is left alone, so a retried job never posts twice.
//   - On success the draft becomes submitted with Procore's id, number and link.
//   - A 422 maps Procore's field errors onto unresolved entries and fails the
//     draft with procore_validation. A lost token, a 401 or a 403 fails it
//     with reauth_required or procore_permission_denied. None are retried.
//   - Any other error may hide a created RFI, so Procore's RFI list is read
//     and an RFI with the same subject created since this attempt began (less
//     recoveryWindow) is taken as this draft's record.
//   - When the latest attempts ended with an unknown outcome (a retry, or a
//     resubmit after the retries ran out), the list is read before posting,
//     since those attempts began, so an RFI listed late is not posted twice.
func (d *Drafts) RunSubmit(ctx context.Context, draftID uuid.UUID) error {
	cur, err := d.load(ctx, draftID)
	if errors.Is(err, ErrNotFound) {
		d.log.Warn("submit job for a missing draft", "draft_id", draftID)
		return nil
	}
	if err != nil {
		return err
	}
	switch cur.Status {
	case StatusSubmitted:
		return nil
	case StatusSubmitting:
	default:
		d.log.Warn("submit job for a draft that is not submitting", "draft_id", draftID, "status", cur.Status)
		return nil
	}
	f, ok := d.features[cur.Feature]
	if !ok {
		return d.failSubmit(ctx, cur, CodeInternal, fmt.Sprintf("unknown feature %q", cur.Feature), nil, nil, 0)
	}

	body, invalid, err := rfiCreate(cur.Payload())
	if err != nil {
		d.log.Error("build procore rfi", "draft_id", cur.ID, "error", err)
		return d.failSubmit(ctx, cur, CodeInternal, "The draft could not be prepared for Procore.", nil, nil, 0)
	}
	if len(invalid) > 0 {
		return d.failSubmit(ctx, cur, CodeProcoreValidation, "Fix the flagged fields and submit again.", invalid, nil, 0)
	}
	request, err := json.Marshal(map[string]procore.RFICreate{"rfi": body})
	if err != nil {
		return fmt.Errorf("pipeline: encode rfi: %w", err)
	}

	token, err := d.tokens.AccessToken(ctx, cur.UserID)
	if errors.Is(err, auth.ErrReauthRequired) {
		return d.failSubmit(ctx, cur, CodeReauthRequired, "Sign in to Procore again, then submit.", nil, nil, 0)
	}
	if err != nil {
		d.auditAttemptFailed(ctx, cur, nil, err, 0)
		return fmt.Errorf("pipeline: access token: %w", err)
	}

	// A retry, or a resubmit after the retries ran out, looks again before
	// posting: an earlier attempt's RFI may only now be listed.
	if since, retrying, err := d.unknownOutcomeSince(ctx, cur.ID); err != nil {
		return err
	} else if retrying {
		if rfi, ok := d.findCreated(ctx, token, cur, body.Subject, since); ok {
			d.log.Info("found rfi from an earlier attempt before retrying", "draft_id", cur.ID, "procore_id", rfi.ID)
			resp := map[string]any{"recovered": true, "before_retry": true, "rfi": rfi}
			return d.markSubmitted(ctx, cur, Result{ProcoreID: rfi.ID, Number: rfi.Number}, nil, resp, 0)
		}
	}

	attemptStart := d.now()
	start := time.Now()
	created, err := d.procore.CreateRFI(ctx, token, cur.CompanyID, cur.ProjectID, body)
	ms := int(time.Since(start).Milliseconds())
	if err == nil {
		return d.markSubmitted(ctx, cur, Result{ProcoreID: created.ID, Number: created.Number, URL: created.Link}, request, created, ms)
	}

	var ve *procore.ValidationError
	switch {
	case errors.As(err, &ve):
		return d.failSubmit(ctx, cur, CodeProcoreValidation, "Procore rejected the RFI. Fix the flagged fields and submit again.",
			procoreFieldErrors(f, ve), request, ms)
	case errors.Is(err, auth.ErrReauthRequired), errors.Is(err, procore.ErrUnauthorized):
		return d.failSubmit(ctx, cur, CodeReauthRequired, "Sign in to Procore again, then submit.", nil, request, ms)
	case errors.Is(err, procore.ErrPermissionDenied):
		return d.failSubmit(ctx, cur, CodePermissionDenied, "Your Procore account cannot create RFIs in this project.", nil, request, ms)
	}

	// The outcome is unknown: Procore may have created the RFI and the
	// response been lost.
	if rfi, ok := d.findCreated(ctx, token, cur, body.Subject, attemptStart); ok {
		d.log.Info("recovered submitted rfi after lost response", "draft_id", cur.ID, "procore_id", rfi.ID, "error", err)
		resp := map[string]any{"recovered": true, "error": err.Error(), "rfi": rfi}
		return d.markSubmitted(ctx, cur, Result{ProcoreID: rfi.ID, Number: rfi.Number}, request, resp, ms)
	}
	d.auditAttemptFailed(ctx, cur, request, err, ms)
	return err
}

// FailSubmit marks a submitting draft failed with procore_unavailable after
// its job has used every attempt. A draft in any other state is left alone.
func (d *Drafts) FailSubmit(ctx context.Context, draftID uuid.UUID, cause error) error {
	cur, err := d.load(ctx, draftID)
	if err != nil {
		return err
	}
	if cur.Status != StatusSubmitting {
		return nil
	}
	return d.failSubmit(ctx, cur, CodeProcoreUnavailable, cause.Error(), nil, nil, 0)
}

// findCreated looks in the project's RFIs for one with subject created at or
// after attemptStart less recoveryWindow, preferring the newest. A failed
// list counts as no match.
func (d *Drafts) findCreated(ctx context.Context, token string, cur Draft, subject string, attemptStart time.Time) (procore.RFI, bool) {
	rfis, err := d.procore.ListRFIs(ctx, token, cur.CompanyID, cur.ProjectID)
	if err != nil {
		d.log.Warn("list rfis for lost-response recovery", "draft_id", cur.ID, "error", err)
		return procore.RFI{}, false
	}
	since := attemptStart.Add(-recoveryWindow)
	var best procore.RFI
	var bestAt time.Time
	found := false
	for _, r := range rfis {
		if r.Subject != subject {
			continue
		}
		at, err := time.Parse(time.RFC3339, r.CreatedAt)
		if err != nil || at.Before(since) {
			continue
		}
		if !found || at.After(bestAt) {
			best, bestAt, found = r, at, true
		}
	}
	return best, found
}

// markSubmitted stores the Procore record and a submitted audit event.
func (d *Drafts) markSubmitted(ctx context.Context, cur Draft, res Result, request json.RawMessage, response any, ms int) error {
	err := d.st.WithTx(ctx, func(q *db.Queries) error {
		if err := q.SetDraftSubmitted(ctx, db.SetDraftSubmittedParams{
			ID:                  pgtype.UUID{Bytes: cur.ID, Valid: true},
			ProcoreRecordID:     pgtype.Int8{Int64: res.ProcoreID, Valid: true},
			ProcoreRecordNumber: pgtype.Text{String: res.Number, Valid: res.Number != ""},
			ProcoreUrl:          pgtype.Text{String: res.URL, Valid: res.URL != ""},
		}); err != nil {
			return fmt.Errorf("pipeline: mark submitted: %w", err)
		}
		return insertProcoreAudit(ctx, q, cur.CompanyID, cur.ID, AuditSubmitted, request, response, ms)
	})
	if err != nil {
		d.log.Error("store submitted draft", "draft_id", cur.ID, "procore_id", res.ProcoreID, "error", err)
	}
	return err
}

// failSubmit marks the draft failed with code and message and writes a failed
// audit event. Entries in invalid replace earlier unresolved entries for the
// same field; the rest are kept. It returns nil once stored, so the job is
// not retried, or the store error.
func (d *Drafts) failSubmit(ctx context.Context, cur Draft, code, message string, invalid []Unresolved, request json.RawMessage, ms int) error {
	unresolved := append([]Unresolved{}, invalid...)
	replaced := map[string]bool{}
	for _, u := range invalid {
		replaced[u.Field] = true
	}
	for _, u := range cur.Unresolved {
		if !replaced[u.Field] {
			unresolved = append(unresolved, u)
		}
	}
	unresolvedJSON, err := json.Marshal(unresolved)
	if err != nil {
		return fmt.Errorf("pipeline: encode unresolved: %w", err)
	}
	response := map[string]any{"error": message}
	if code == CodeProcoreUnavailable {
		response["unknown_outcome"] = true
	}
	if len(invalid) > 0 {
		response["fields"] = invalid
	}
	err = d.st.WithTx(ctx, func(q *db.Queries) error {
		if err := q.SetDraftFailed(ctx, db.SetDraftFailedParams{
			ID:           pgtype.UUID{Bytes: cur.ID, Valid: true},
			ErrorCode:    pgtype.Text{String: code, Valid: true},
			ErrorMessage: pgtype.Text{String: message, Valid: true},
			Unresolved:   unresolvedJSON,
		}); err != nil {
			return fmt.Errorf("pipeline: mark failed: %w", err)
		}
		return insertProcoreAudit(ctx, q, cur.CompanyID, cur.ID, AuditFailed, request, response, ms)
	})
	if err != nil {
		d.log.Error("store failed draft", "draft_id", cur.ID, "code", code, "error", err)
		return err
	}
	d.log.Info("draft submit failed", "draft_id", cur.ID, "code", code)
	return nil
}

// unknownOutcome is the procore_response of a failed attempt whose RFI may
// nonetheless exist: one River will retry, or the procore_unavailable
// failure after the last retry.
type unknownOutcome struct {
	Error          string `json:"error"`
	UnknownOutcome bool   `json:"unknown_outcome"`
}

// unknownOutcomeSince reports whether the draft's latest audit events are
// failed attempts with an unknown outcome, and if so when the earliest of
// that run began: its event time less its duration. Any other event, such as
// an edit, ends the run.
func (d *Drafts) unknownOutcomeSince(ctx context.Context, draftID uuid.UUID) (time.Time, bool, error) {
	evs, err := d.st.Q.ListAuditEvents(ctx, pgtype.UUID{Bytes: draftID, Valid: true})
	if err != nil {
		return time.Time{}, false, fmt.Errorf("pipeline: list audit events: %w", err)
	}
	var since time.Time
	found := false
	for i := len(evs) - 1; i >= 0; i-- {
		ev := evs[i]
		var r unknownOutcome
		if ev.Kind != AuditFailed || json.Unmarshal(ev.ProcoreResponse, &r) != nil || !r.UnknownOutcome {
			break
		}
		since = ev.CreatedAt.Time.Add(-time.Duration(ev.DurationMs.Int32) * time.Millisecond)
		found = true
	}
	return since, found, nil
}

// auditAttemptFailed records an attempt that will be retried. The draft
// stays submitting. A store error is logged, not returned, so the attempt's
// own error reaches River.
func (d *Drafts) auditAttemptFailed(ctx context.Context, cur Draft, request json.RawMessage, cause error, ms int) {
	err := insertProcoreAudit(ctx, d.st.Q, cur.CompanyID, cur.ID, AuditFailed, request, unknownOutcome{Error: cause.Error(), UnknownOutcome: true}, ms)
	if err != nil {
		d.log.Error("audit failed submit attempt", "draft_id", cur.ID, "error", err)
	}
	d.log.Warn("submit attempt failed", "draft_id", cur.ID, "error", cause)
}

// procoreFieldErrors maps a 422's field errors onto payload fields: a leading
// "rfi." is dropped, "question" and "question.body" become question_body, a
// payload field keeps its name, and anything else is reported on FormField
// with its key. Messages for one field are joined with "; ".
func procoreFieldErrors(f Feature, ve *procore.ValidationError) []Unresolved {
	known := map[string]bool{}
	for _, field := range f.PayloadFields() {
		known[field] = true
	}
	keys := make([]string, 0, len(ve.Fields))
	for k := range ve.Fields {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	msgs := map[string][]string{}
	var order []string
	for _, key := range keys {
		k := strings.TrimPrefix(key, "rfi.")
		field := k
		switch {
		case k == "question" || k == "question.body":
			field = "question_body"
		case !known[k]:
			field = FormField
		}
		if _, seen := msgs[field]; !seen {
			order = append(order, field)
		}
		for _, m := range ve.Fields[key] {
			if field == FormField && k != "base" {
				m = k + ": " + m
			}
			msgs[field] = append(msgs[field], m)
		}
	}
	out := make([]Unresolved, 0, len(order))
	for _, field := range order {
		out = append(out, Unresolved{
			Field:      field,
			Reason:     ReasonProcoreValidation,
			Message:    strings.Join(msgs[field], "; "),
			Candidates: []LookupRow{},
		})
	}
	return out
}

// rfiPayload is the stored RFI draft payload, as the contract's
// RFIDraftPayload.
type rfiPayload struct {
	Subject                 string         `json:"subject"`
	QuestionBody            string         `json:"question_body"`
	AssigneeIDs             []int64        `json:"assignee_ids"`
	RFIManagerID            *int64         `json:"rfi_manager_id"`
	DistributionIDs         []int64        `json:"distribution_ids"`
	ResponsibleContractorID *int64         `json:"responsible_contractor_id"`
	LocationID              *int64         `json:"location_id"`
	SpecificationSectionID  *int64         `json:"specification_section_id"`
	DrawingNumber           string         `json:"drawing_number"`
	CostCodeID              *int64         `json:"cost_code_id"`
	DueDate                 string         `json:"due_date"`
	ScheduleImpact          *payloadImpact `json:"schedule_impact"`
	CostImpact              *payloadImpact `json:"cost_impact"`
	Private                 *bool          `json:"private"`
	Draft                   *bool          `json:"draft"`
}

type payloadImpact struct {
	Status string `json:"status"`
	Value  string `json:"value"`
}

// rfiCreate builds the Procore body from a stored payload. Empty strings and
// absent fields are omitted. An impact value that is not a number is
// returned as an unresolved entry for that impact field.
func rfiCreate(raw json.RawMessage) (procore.RFICreate, []Unresolved, error) {
	var p rfiPayload
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &p); err != nil {
			return procore.RFICreate{}, nil, fmt.Errorf("pipeline: decode rfi payload: %w", err)
		}
	}
	body := procore.RFICreate{
		Subject:                 p.Subject,
		Question:                procore.RFIQuestion{Body: p.QuestionBody},
		AssigneeIDs:             p.AssigneeIDs,
		RFIManagerID:            p.RFIManagerID,
		DistributionIDs:         p.DistributionIDs,
		ResponsibleContractorID: p.ResponsibleContractorID,
		LocationID:              p.LocationID,
		SpecificationSectionID:  p.SpecificationSectionID,
		CostCodeID:              p.CostCodeID,
		Private:                 p.Private,
		Draft:                   p.Draft,
	}
	if s := strings.TrimSpace(p.DrawingNumber); s != "" {
		body.DrawingNumber = &s
	}
	if s := strings.TrimSpace(p.DueDate); s != "" {
		body.DueDate = &s
	}
	var invalid []Unresolved
	impact := func(field string, in *payloadImpact) *procore.RFIImpact {
		if in == nil || in.Status == "" {
			return nil
		}
		out := &procore.RFIImpact{Status: in.Status}
		if s := strings.TrimSpace(in.Value); s != "" {
			v, err := strconv.ParseFloat(s, 64)
			if err != nil || math.IsNaN(v) || math.IsInf(v, 0) {
				invalid = append(invalid, Unresolved{
					Field: field, Reason: ReasonProcoreValidation, Message: "value must be a number", Candidates: []LookupRow{},
				})
				return nil
			}
			out.Value = &v
		}
		return out
	}
	body.ScheduleImpact = impact("schedule_impact", p.ScheduleImpact)
	body.CostImpact = impact("cost_impact", p.CostImpact)
	return body, invalid, nil
}
