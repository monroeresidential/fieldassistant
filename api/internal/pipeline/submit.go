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
	"github.com/jackc/pgx/v5"
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
	// postWriteTimeout bounds the store and audit writes after a POST,
	// which run detached from the job's context.
	postWriteTimeout = 15 * time.Second
)

// UnavailableMessage is the error message a draft stores when its submit
// ran out of attempts; the cause is logged and audited, never shown.
const UnavailableMessage = "Procore could not be reached. Try submitting again."

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
// with *ErrUnresolved and leaves the draft as it was.
//
// A valid draft moves to submitting only if it is unchanged since it was
// validated and no other Submit got there first (MarkDraftSubmitting); then
// enqueue is called and the job ID stored. When the transition loses, the
// draft is read again: a concurrent Submit's outcome is returned, and a
// concurrent edit is validated once more. A draft edited again during that
// second pass is ErrDraftChanged.
//
// enqueue must be idempotent per draft (the River queue dedupes by draft ID):
// a Submit that finds the draft submitting before its job ID is stored
// enqueues again to learn the job.
func (d *Drafts) Submit(ctx context.Context, userID, id uuid.UUID, enqueue func(ctx context.Context, draftID uuid.UUID) (int64, error)) (SubmitOutcome, error) {
	pid := pgtype.UUID{Bytes: id, Valid: true}
	for pass := 0; ; pass++ {
		cur, err := d.Get(ctx, userID, id)
		if err != nil {
			return SubmitOutcome{}, err
		}
		switch {
		case cur.Status == StatusSubmitted:
			return SubmitOutcome{JobID: cur.JobID, Draft: cur, AlreadySubmitted: true}, nil
		case cur.Status == StatusSubmitting && cur.JobID != 0:
			return SubmitOutcome{JobID: cur.JobID, Draft: cur}, nil
		case cur.Status == StatusSubmitting:
			// Another Submit marked it and has not stored its job yet, or
			// crashed before enqueueing. Enqueueing is idempotent per draft.
			return d.enqueueSubmit(ctx, id, enqueue, "")
		case pass == 2:
			return SubmitOutcome{}, ErrDraftChanged
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
		if d.beforeMarkSubmitting != nil {
			d.beforeMarkSubmitting()
		}

		_, err = d.st.Q.MarkDraftSubmitting(ctx, db.MarkDraftSubmittingParams{
			ID: pid, UpdatedAt: pgtype.Timestamptz{Time: cur.UpdatedAt, Valid: true},
		})
		if errors.Is(err, pgx.ErrNoRows) {
			continue // another Submit or an edit won; look again
		}
		if err != nil {
			return SubmitOutcome{}, fmt.Errorf("pipeline: mark submitting: %w", err)
		}
		return d.enqueueSubmit(ctx, id, enqueue, cur.Status)
	}
}

// enqueueSubmit enqueues the submitting draft's job and stores its ID. If
// enqueue fails and restore is set, the draft goes back to that status so
// the user can edit or retry.
func (d *Drafts) enqueueSubmit(ctx context.Context, id uuid.UUID, enqueue func(ctx context.Context, draftID uuid.UUID) (int64, error), restore string) (SubmitOutcome, error) {
	pid := pgtype.UUID{Bytes: id, Valid: true}
	jobID, err := enqueue(ctx, id)
	if err != nil {
		if restore != "" {
			if rerr := d.st.Q.SetDraftStatus(ctx, db.SetDraftStatusParams{ID: pid, Status: restore}); rerr != nil {
				d.log.Error("restore draft status after enqueue failure", "draft_id", id, "error", rerr)
			}
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
//   - Before posting, an attempt audit event is written; if that write fails
//     the attempt stops without posting. A crash or a lost write after the
//     POST therefore always leaves evidence for the next attempt.
//   - When the latest audit events are attempts with an unknown outcome (a
//     retry, a resubmit after the retries ran out, or a crash mid-POST),
//     Procore's RFI list is read before posting and a matching RFI (see
//     findCreated) is adopted instead of posting. Several matches, or a list
//     that cannot be read, stop the attempt without posting.
//   - On success the draft becomes submitted with Procore's id, number and
//     link. Writes after the POST outlive the job's context, so a deadline
//     cannot lose a created RFI.
//   - A 422 maps Procore's field errors onto unresolved entries and fails the
//     draft with procore_validation. A lost token, a 401 or a 403 fails it
//     with reauth_required or procore_permission_denied. None are retried.
//   - Any other error may hide a created RFI, so the list is read and a
//     single matching RFI created since this attempt began is adopted.
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
		return d.failSubmit(ctx, cur, CodeInternal, fmt.Sprintf("unknown feature %q", cur.Feature), nil, nil, nil, 0)
	}

	body, invalid, err := rfiCreate(cur.Payload())
	if err != nil {
		return d.failSubmit(ctx, cur, CodeInternal, "The draft could not be prepared for Procore.", err, nil, nil, 0)
	}
	if len(invalid) > 0 {
		return d.failSubmit(ctx, cur, CodeProcoreValidation, "Fix the flagged fields and submit again.", nil, invalid, nil, 0)
	}
	request, err := json.Marshal(map[string]procore.RFICreate{"rfi": body})
	if err != nil {
		return fmt.Errorf("pipeline: encode rfi: %w", err)
	}
	user, err := d.st.Q.GetUser(ctx, pgtype.UUID{Bytes: cur.UserID, Valid: true})
	if err != nil {
		return fmt.Errorf("pipeline: load draft user: %w", err)
	}

	token, err := d.tokens.AccessToken(ctx, cur.UserID)
	if errors.Is(err, auth.ErrReauthRequired) {
		return d.failSubmit(ctx, cur, CodeReauthRequired, "Sign in to Procore again, then submit.", err, nil, nil, 0)
	}
	if err != nil {
		d.auditAttemptFailed(ctx, cur, nil, err, 0)
		return fmt.Errorf("pipeline: access token: %w", err)
	}

	prior, err := d.priorAttempts(ctx, cur.ID)
	if err != nil {
		return err
	}
	if prior.unknown {
		rfi, n, err := d.findCreated(ctx, token, cur, user.ProcoreUserID, body.Subject, prior.since)
		switch {
		case err != nil:
			d.auditAttemptFailed(ctx, cur, nil, err, 0)
			return fmt.Errorf("pipeline: look for an earlier attempt's rfi: %w", err)
		case n == 1:
			d.log.Info("found rfi from an earlier attempt before posting", "draft_id", cur.ID, "procore_id", rfi.ID)
			resp := map[string]any{"recovered": true, "before_post": true, "rfi": rfi}
			return d.markSubmitted(ctx, cur, Result{ProcoreID: rfi.ID, Number: rfi.Number}, nil, resp, 0)
		case n > 1:
			err := fmt.Errorf("pipeline: %d rfis could be an earlier attempt's; not posting again", n)
			d.auditAttemptFailed(ctx, cur, nil, err, 0)
			return err
		}
	}

	attemptStart := d.now()
	if err := d.writeAttempt(ctx, cur, prior.attempts+1, attemptStart); err != nil {
		return err
	}
	start := time.Now()
	created, err := d.procore.CreateRFI(ctx, token, cur.CompanyID, cur.ProjectID, body)
	ms := int(time.Since(start).Milliseconds())

	// Record the outcome even if the job's context is done.
	wctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), postWriteTimeout)
	defer cancel()
	if err == nil {
		return d.markSubmitted(wctx, cur, Result{ProcoreID: created.ID, Number: created.Number, URL: created.Link}, request, created, ms)
	}

	var ve *procore.ValidationError
	switch {
	case errors.As(err, &ve):
		return d.failSubmit(wctx, cur, CodeProcoreValidation, "Procore rejected the RFI. Fix the flagged fields and submit again.",
			err, procoreFieldErrors(f, ve), request, ms)
	case errors.Is(err, auth.ErrReauthRequired), errors.Is(err, procore.ErrUnauthorized):
		return d.failSubmit(wctx, cur, CodeReauthRequired, "Sign in to Procore again, then submit.", err, nil, request, ms)
	case errors.Is(err, procore.ErrPermissionDenied):
		return d.failSubmit(wctx, cur, CodePermissionDenied, "Your Procore account cannot create RFIs in this project.", err, nil, request, ms)
	}

	// The outcome is unknown: Procore may have created the RFI and the
	// response been lost.
	rfi, n, lerr := d.findCreated(wctx, token, cur, user.ProcoreUserID, body.Subject, attemptStart)
	if lerr == nil && n == 1 {
		d.log.Info("recovered submitted rfi after lost response", "draft_id", cur.ID, "procore_id", rfi.ID, "error", err)
		resp := map[string]any{"recovered": true, "error": err.Error(), "rfi": rfi}
		return d.markSubmitted(wctx, cur, Result{ProcoreID: rfi.ID, Number: rfi.Number}, request, resp, ms)
	}
	if lerr != nil {
		d.log.Warn("list rfis for lost-response recovery", "draft_id", cur.ID, "error", lerr)
	}
	d.auditAttemptFailed(wctx, cur, request, err, ms)
	return err
}

// FailSubmit marks a submitting draft failed with procore_unavailable after
// its job has used every attempt. The draft stores a fixed message; the
// cause goes to the log and the audit event only. A draft in any other state
// is left alone.
func (d *Drafts) FailSubmit(ctx context.Context, draftID uuid.UUID, cause error) error {
	cur, err := d.load(ctx, draftID)
	if err != nil {
		return err
	}
	if cur.Status != StatusSubmitting {
		return nil
	}
	return d.failSubmit(ctx, cur, CodeProcoreUnavailable, UnavailableMessage, cause, nil, nil, 0)
}

// findCreated looks in the project's RFIs for ones that could be this
// draft's: same subject exactly, created by the draft user's Procore account
// (creatorID), at or after since less recoveryWindow. It returns the match
// and how many there are; only exactly one may be adopted. A list error is
// returned as is.
func (d *Drafts) findCreated(ctx context.Context, token string, cur Draft, creatorID int64, subject string, since time.Time) (procore.RFI, int, error) {
	rfis, err := d.procore.ListRFIs(ctx, token, cur.CompanyID, cur.ProjectID)
	if err != nil {
		return procore.RFI{}, 0, err
	}
	from := since.Add(-recoveryWindow)
	var match procore.RFI
	n := 0
	for _, r := range rfis {
		if r.Subject != subject || r.CreatedBy.ID != creatorID {
			continue
		}
		at, err := time.Parse(time.RFC3339, r.CreatedAt)
		if err != nil || at.Before(from) {
			continue
		}
		match = r
		n++
	}
	if n > 1 {
		d.log.Warn("several rfis match a submit; adopting none", "draft_id", cur.ID, "matches", n)
	}
	return match, n, nil
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

// failSubmit marks the draft failed with code and the user-facing message
// and writes a failed audit event carrying cause. Entries in invalid replace
// earlier unresolved entries for the same field; the rest are kept. It
// returns nil once stored, so the job is not retried, or the store error.
func (d *Drafts) failSubmit(ctx context.Context, cur Draft, code, message string, cause error, invalid []Unresolved, request json.RawMessage, ms int) error {
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
	if cause != nil {
		response["cause"] = cause.Error()
	}
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
	d.log.Info("draft submit failed", "draft_id", cur.ID, "code", code, "cause", cause)
	return nil
}

// attemptAudit is the payload of an attempt audit event, written just before
// a POST.
type attemptAudit struct {
	Attempt int       `json:"attempt"`
	At      time.Time `json:"at"`
}

// writeAttempt records that attempt n is about to post. An error stops the
// attempt before the POST.
func (d *Drafts) writeAttempt(ctx context.Context, cur Draft, n int, at time.Time) error {
	if err := insertAudit(ctx, d.st.Q, cur.CompanyID, cur.ID, AuditAttempt, attemptAudit{Attempt: n, At: at}, 0); err != nil {
		return fmt.Errorf("pipeline: write submit attempt marker: %w", err)
	}
	return nil
}

// unknownOutcome is the procore_response of a failed attempt whose RFI may
// nonetheless exist: one River will retry, or the procore_unavailable
// failure after the last retry.
type unknownOutcome struct {
	Error          string `json:"error"`
	UnknownOutcome bool   `json:"unknown_outcome"`
}

// attemptHistory summarises a draft's audit trail for RunSubmit.
type attemptHistory struct {
	// attempts counts every attempt marker the draft has.
	attempts int
	// unknown is true when the latest audit events are attempt markers and
	// failures with an unknown outcome, i.e. an earlier POST may have
	// created an RFI; since is when the earliest of that run began.
	unknown bool
	since   time.Time
}

// priorAttempts reads the draft's audit trail. A run of unknown-outcome
// events ends at any other event, such as a 422 failure or an edit.
func (d *Drafts) priorAttempts(ctx context.Context, draftID uuid.UUID) (attemptHistory, error) {
	evs, err := d.st.Q.ListAuditEvents(ctx, pgtype.UUID{Bytes: draftID, Valid: true})
	if err != nil {
		return attemptHistory{}, fmt.Errorf("pipeline: list audit events: %w", err)
	}
	var h attemptHistory
	for _, ev := range evs {
		if ev.Kind == AuditAttempt {
			h.attempts++
		}
	}
	for i := len(evs) - 1; i >= 0; i-- {
		ev := evs[i]
		var at time.Time
		switch ev.Kind {
		case AuditAttempt:
			var a attemptAudit
			if json.Unmarshal(ev.Payload, &a) != nil || a.At.IsZero() {
				a.At = ev.CreatedAt.Time
			}
			at = a.At
		case AuditFailed:
			var r unknownOutcome
			if json.Unmarshal(ev.ProcoreResponse, &r) != nil || !r.UnknownOutcome {
				return h, nil
			}
			at = ev.CreatedAt.Time.Add(-time.Duration(ev.DurationMs.Int32) * time.Millisecond)
		default:
			return h, nil
		}
		if !h.unknown || at.Before(h.since) {
			h.since = at
		}
		h.unknown = true
	}
	return h, nil
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
