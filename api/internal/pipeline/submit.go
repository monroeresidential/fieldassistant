package pipeline

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
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

// AmbiguousRFIMessage is the _form message of a draft whose submit found
// several RFIs that could be its own and adopted none.
const AmbiguousRFIMessage = "An RFI with this subject already exists in Procore; change the subject or check Procore"

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

// SubmitQueue queues submit jobs. *jobs.Queue satisfies it.
type SubmitQueue interface {
	// EnqueueTx inserts the submit job for attempt (the draft's
	// submit_attempts after claiming it) of draftID in tx and returns its ID.
	// While that attempt already has an active job, that job's ID is
	// returned; a new attempt always gets a new job.
	EnqueueTx(ctx context.Context, tx pgx.Tx, draftID uuid.UUID, attempt int) (int64, error)
	// JobFinished reports whether jobID will never run again: it completed,
	// was discarded or cancelled, or no longer exists.
	JobFinished(ctx context.Context, jobID int64) (bool, error)
}

// maxSubmitPasses bounds Submit's re-reads after losing a race.
const maxSubmitPasses = 3

// Submit queues the user's draft for posting to Procore. A submitted draft
// returns AlreadySubmitted; a submitting draft returns its job while that
// job can still run. Otherwise the payload is validated against freshly
// warmed lookups (see revalidate), and any blocking unresolved field (see
// markBlocking), including an earlier flag on a field the reviewer has not
// changed, fails with *ErrUnresolved. Advisory flags do not stop the submit
// and stay on the draft. When that validation differs from what the draft
// stores, it is stored first as SaveEdits would, so the draft shows the
// fields the error names.
//
// A valid draft moves to submitting, its job is inserted and the job ID
// stored in one transaction, so a draft is never submitting without a job
// and a failed insert leaves it as it was. The transition happens only if
// the draft is unchanged since it was validated and no other Submit got
// there first (MarkDraftSubmitting). When it loses, the draft is read again:
// a concurrent Submit's outcome is returned, and a concurrent edit is
// validated once more. A draft edited again during that second pass is
// ErrDraftChanged.
//
// A submitting draft whose job finished without settling it (discarded,
// cancelled, or gone) gets a new job, which runs RunSubmit's check for an
// earlier attempt's RFI before posting.
//
// A draft that is not yet submitting or submitted with any attachment
// pending or failed is refused with *PendingError (ErrAttachmentsPending),
// before anything is validated or stored. A submitting or submitted draft
// answers as above whatever its attachments' states.
func (d *Drafts) Submit(ctx context.Context, userID, id uuid.UUID, q SubmitQueue) (SubmitOutcome, error) {
	pid := pgtype.UUID{Bytes: id, Valid: true}
	edits := 0
	for pass := 0; pass < maxSubmitPasses; pass++ {
		cur, err := d.Get(ctx, userID, id)
		if err != nil {
			return SubmitOutcome{}, err
		}
		switch {
		case cur.Status == StatusSubmitted:
			return SubmitOutcome{JobID: cur.JobID, Draft: cur, AlreadySubmitted: true}, nil
		case cur.Status == StatusSubmitting:
			out, replaced, err := d.reviveSubmit(ctx, cur, q)
			if err != nil || replaced {
				return out, err
			}
			continue // another Submit replaced the job first; look again
		case edits == 2:
			return SubmitOutcome{}, ErrDraftChanged
		}
		if err := d.checkAttachments(ctx, id); err != nil {
			return SubmitOutcome{}, err
		}
		edits++

		var payload map[string]any
		if raw := cur.Payload(); len(raw) > 0 {
			if err := json.Unmarshal(raw, &payload); err != nil {
				return SubmitOutcome{}, fmt.Errorf("pipeline: decode payload: %w", err)
			}
		}
		validated, unresolved, err := d.revalidate(ctx, userID, cur, payload)
		if err != nil {
			return SubmitOutcome{}, err
		}
		if len(unresolved) > 0 && (!sameJSON(payload, validated) || !sameJSON(cur.Unresolved, unresolved)) {
			// Store what this validation found (a due date that has since
			// passed, an ID gone from the lookups), so the draft shows the
			// fields the error names, and so a draft submitted with only
			// advisory flags posts the payload those flags describe.
			saved, err := d.storeEdits(ctx, cur, validated, unresolved, true)
			if errors.Is(err, pgx.ErrNoRows) {
				continue // edited or submitted meanwhile; look again
			}
			if err != nil {
				return SubmitOutcome{}, err
			}
			cur = saved
		}
		if fields := blockingFields(unresolved); len(fields) > 0 {
			return SubmitOutcome{}, &ErrUnresolved{Fields: fields}
		}
		// Advisory flags stay on the draft; the review screen still shows
		// them after the submit.
		if d.beforeMarkSubmitting != nil {
			d.beforeMarkSubmitting()
		}

		var jobID int64
		err = d.st.WithPgxTx(ctx, func(tx pgx.Tx, qtx *db.Queries) error {
			row, err := qtx.MarkDraftSubmitting(ctx, db.MarkDraftSubmittingParams{
				ID: pid, UpdatedAt: pgtype.Timestamptz{Time: cur.UpdatedAt, Valid: true},
			})
			if err != nil {
				return err
			}
			if jobID, err = q.EnqueueTx(ctx, tx, id, int(row.SubmitAttempts)); err != nil {
				return fmt.Errorf("pipeline: enqueue submit: %w", err)
			}
			return qtx.SetDraftJob(ctx, db.SetDraftJobParams{ID: pid, JobID: pgtype.Int8{Int64: jobID, Valid: true}})
		})
		if errors.Is(err, pgx.ErrNoRows) {
			continue // another Submit or an edit won; look again
		}
		if err != nil {
			return SubmitOutcome{}, fmt.Errorf("pipeline: mark submitting: %w", err)
		}
		after, err := d.load(ctx, id)
		if err != nil {
			return SubmitOutcome{}, err
		}
		return SubmitOutcome{JobID: jobID, Draft: after}, nil
	}
	return SubmitOutcome{}, ErrDraftChanged
}

// reviveSubmit handles Submit on a submitting draft. While its job can still
// run, that job is returned. Once the job has finished without settling the
// draft, a new attempt is claimed and its job inserted and stored in one
// transaction, provided the draft still holds the finished job; replaced is
// false when another Submit changed it first, and the caller reads the
// draft again.
func (d *Drafts) reviveSubmit(ctx context.Context, cur Draft, q SubmitQueue) (out SubmitOutcome, replaced bool, err error) {
	if cur.JobID != 0 {
		finished, err := q.JobFinished(ctx, cur.JobID)
		if err != nil {
			return SubmitOutcome{}, false, fmt.Errorf("pipeline: submit job state: %w", err)
		}
		if !finished {
			return SubmitOutcome{JobID: cur.JobID, Draft: cur}, true, nil
		}
	}
	d.log.Warn("submitting draft has no live job; queueing a new one", "draft_id", cur.ID, "old_job_id", cur.JobID)
	var jobID int64
	err = d.st.WithPgxTx(ctx, func(tx pgx.Tx, qtx *db.Queries) error {
		pid := pgtype.UUID{Bytes: cur.ID, Valid: true}
		attempt, err := qtx.ClaimDraftResubmit(ctx, db.ClaimDraftResubmitParams{
			ID:       pid,
			OldJobID: pgtype.Int8{Int64: cur.JobID, Valid: cur.JobID != 0},
		})
		if err != nil {
			return err // pgx.ErrNoRows: another Submit got there first
		}
		if jobID, err = q.EnqueueTx(ctx, tx, cur.ID, int(attempt)); err != nil {
			return fmt.Errorf("pipeline: enqueue submit: %w", err)
		}
		if err := qtx.SetDraftJob(ctx, db.SetDraftJobParams{ID: pid, JobID: pgtype.Int8{Int64: jobID, Valid: true}}); err != nil {
			return fmt.Errorf("pipeline: store job id: %w", err)
		}
		return nil
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return SubmitOutcome{}, false, nil
	}
	if err != nil {
		return SubmitOutcome{}, false, err
	}
	after, err := d.load(ctx, cur.ID)
	if err != nil {
		return SubmitOutcome{}, false, err
	}
	return SubmitOutcome{JobID: jobID, Draft: after}, true, nil
}

// revalidate runs Validate on payload against freshly warmed lookups, then
// again with cur's earlier unresolved entries carried forward, so a flag
// the model, an earlier validation or a Procore 422 raised is kept until the
// reviewer deals with it. An entry is carried while its field is still
// empty or still holds the value cur stored when the flag was raised
// (compared as normalized JSON); a field whose value changed keeps only
// what Validate says of the new value. Validate's own entries come first
// and win.
func (d *Drafts) revalidate(ctx context.Context, userID uuid.UUID, cur Draft, payload map[string]any) (map[string]any, []Unresolved, error) {
	f, ok := d.features[cur.Feature]
	if !ok {
		return nil, nil, fmt.Errorf("pipeline: unknown feature %q", cur.Feature)
	}
	sets, failed, err := d.warm(ctx, userID, cur.CompanyID, cur.ProjectID, f)
	if err != nil {
		return nil, nil, err
	}
	// An ID cannot be judged without its list: rather than drop or flag a
	// reviewer's choice during an outage, fail this call. Kinds no field
	// uses (drawings for an RFI) never block.
	for _, idf := range f.IDFields() {
		if kerr, ok := failed[idf.Kind]; ok && !isEmptyValue(payload[idf.Field]) {
			return nil, nil, fmt.Errorf("pipeline: %s lookup needed for %s: %w", idf.Kind, idf.Field, kerr)
		}
	}
	project, err := d.lookups.Project(ctx, userID, cur.CompanyID, cur.ProjectID)
	if err != nil {
		return nil, nil, err
	}
	today, loc := d.now(), RailsZone(project.TimeZone)
	var stored map[string]any
	if raw := cur.Payload(); len(raw) > 0 {
		if err := json.Unmarshal(raw, &stored); err != nil {
			return nil, nil, fmt.Errorf("pipeline: decode stored payload: %w", err)
		}
	}
	stored, _ = f.Normalize(stored, nil)
	normalized, flagged := f.Normalize(payload, nil)
	validated, unresolved := Validate(f, normalized, flagged, sets, today, loc)
	for _, u := range cur.Unresolved {
		if isEmptyValue(validated[u.Field]) || sameJSON(stored[u.Field], normalized[u.Field]) {
			unresolved = append(unresolved, u)
		}
	}
	validated, unresolved = Validate(f, validated, unresolved, sets, today, loc)
	return validated, markBlocking(f, unresolved), nil
}

// RunSubmit is the submit job body: it posts a submitting draft to Procore
// as the draft's user and records the outcome. It returns an error only when
// the outcome is unknown and a retry may succeed; River retries the job then.
//
//   - A submitted draft is left alone, so a retried job never posts twice.
//   - Before posting, an attempt audit event is written; if that write fails
//     the attempt stops without posting. A crash or a lost write after the
//     POST therefore always leaves evidence for the next attempt.
//   - When an earlier attempt since the last submitted event may have
//     created an RFI (see priorAttempts: a retry, a resubmit after the
//     retries ran out, with or without edits in between, or a crash
//     mid-POST), Procore's RFI list is read before posting and a matching
//     RFI (see findCreated) is adopted instead of posting. A list that
//     cannot be read stops the attempt without posting; several matches
//     fail the draft definitively (failAmbiguous).
//   - On success the draft becomes submitted with Procore's id, number and
//     link. Writes after the POST outlive the job's context, so a deadline
//     cannot lose a created RFI.
//   - A 422 maps Procore's field errors onto unresolved entries and fails the
//     draft with procore_validation; any other 4xx except 408 and 429 does
//     the same with Procore's message on _form. A lost token, a 401 or a 403
//     fails it with reauth_required or procore_permission_denied. None are
//     retried.
//   - Any other error may hide a created RFI, so the list is read and a
//     single matching RFI created since this attempt began is adopted;
//     several fail the draft definitively.
//   - A Procore record belongs to one draft (drafts_procore_record_uidx).
//     When another draft stores a matching RFI first, a recovery treats it
//     as no match; a POST whose own RFI was taken that way fails the draft
//     with AmbiguousRFIMessage rather than post again.
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
		return d.failSubmit(ctx, cur, submitFailure{code: CodeInternal, message: fmt.Sprintf("unknown feature %q", cur.Feature)})
	}

	body, invalid, err := rfiCreate(cur.Payload())
	if err != nil {
		return d.failSubmit(ctx, cur, submitFailure{code: CodeInternal, message: "The draft could not be prepared for Procore.", cause: err})
	}
	if len(invalid) > 0 {
		return d.failSubmit(ctx, cur, submitFailure{code: CodeProcoreValidation, message: "Fix the flagged fields and submit again.", invalid: invalid})
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
		return d.failSubmit(ctx, cur, submitFailure{code: CodeReauthRequired, message: "Sign in to Procore again, then submit.", cause: err})
	}
	if err != nil {
		d.auditAttemptFailed(ctx, cur, nil, err, 0, false)
		return fmt.Errorf("pipeline: access token: %w", err)
	}

	prior, err := d.priorAttempts(ctx, cur.ID)
	if err != nil {
		return err
	}
	if prior.unknown {
		subjects := append([]string{body.Subject}, prior.subjects...)
		rfi, n, err := d.findCreated(ctx, token, cur, user.ProcoreUserID, subjects, prior.since)
		switch {
		case err != nil:
			d.auditAttemptFailed(ctx, cur, nil, err, 0, false)
			return fmt.Errorf("pipeline: look for an earlier attempt's rfi: %w", err)
		case n == 1:
			d.log.Info("found rfi from an earlier attempt before posting", "draft_id", cur.ID, "procore_id", rfi.ID)
			resp := map[string]any{"recovered": true, "before_post": true, "rfi": rfi}
			err := d.markSubmitted(ctx, cur, Result{ProcoreID: rfi.ID, Number: rfi.Number, URL: rfi.Link}, nil, resp, 0)
			if !errors.Is(err, errRecordOwned) {
				return err
			}
			// Another draft adopted it between the list and the store:
			// it was the only match, so there is none; post.
		case n > 1:
			return d.failAmbiguous(ctx, cur, n, nil, 0)
		}
	}

	attemptStart := d.now()
	if err := d.writeAttempt(ctx, cur, prior.attempts+1, body.Subject, attemptStart); err != nil {
		return err
	}
	start := time.Now()
	created, err := d.procore.CreateRFI(ctx, token, cur.CompanyID, cur.ProjectID, body)
	ms := int(time.Since(start).Milliseconds())

	// Record the outcome even if the job's context is done.
	wctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), postWriteTimeout)
	defer cancel()
	if err == nil {
		err := d.markSubmitted(wctx, cur, Result{ProcoreID: created.ID, Number: created.Number, URL: created.Link}, request, created, ms)
		if errors.Is(err, errRecordOwned) {
			// Another draft's recovery adopted this POST's RFI before it
			// was stored. Retrying would post again, so tell the user to
			// check Procore instead.
			return d.failSubmit(wctx, cur, submitFailure{
				code:    CodeProcoreValidation,
				message: AmbiguousRFIMessage,
				cause:   fmt.Errorf("pipeline: created rfi %d is stored by another draft", created.ID),
				invalid: []Unresolved{{Field: FormField, Reason: ReasonProcoreValidation, Message: AmbiguousRFIMessage, Candidates: []LookupRow{}}},
				request: request, ms: ms,
				closesRun: true,
			})
		}
		return err
	}

	// Procore answered and created nothing: fail the draft, do not retry.
	rejected := submitFailure{cause: err, request: request, ms: ms, rejected: true}
	var ve *procore.ValidationError
	var api *procore.APIError
	switch {
	case errors.As(err, &ve):
		rejected.code, rejected.message = CodeProcoreValidation, "Procore rejected the RFI. Fix the flagged fields and submit again."
		rejected.invalid = procoreFieldErrors(f, ve)
		return d.failSubmit(wctx, cur, rejected)
	case errors.Is(err, auth.ErrReauthRequired), errors.Is(err, procore.ErrUnauthorized):
		rejected.code, rejected.message = CodeReauthRequired, "Sign in to Procore again, then submit."
		return d.failSubmit(wctx, cur, rejected)
	case errors.Is(err, procore.ErrPermissionDenied):
		rejected.code, rejected.message = CodePermissionDenied, "Your Procore account cannot create RFIs in this project."
		return d.failSubmit(wctx, cur, rejected)
	case errors.As(err, &api) && definitiveStatus(api.Status):
		rejected.code, rejected.message = CodeProcoreValidation, "Procore rejected the RFI. Fix the flagged fields and submit again."
		rejected.invalid = []Unresolved{{Field: FormField, Reason: ReasonProcoreValidation, Message: rejectionMessage(api), Candidates: []LookupRow{}}}
		return d.failSubmit(wctx, cur, rejected)
	}

	// The outcome is unknown: Procore may have created the RFI and the
	// response been lost.
	rfi, n, lerr := d.findCreated(wctx, token, cur, user.ProcoreUserID, []string{body.Subject}, attemptStart)
	switch {
	case lerr != nil:
		d.log.Warn("list rfis for lost-response recovery", "draft_id", cur.ID, "error", lerr)
	case n == 1:
		d.log.Info("recovered submitted rfi after lost response", "draft_id", cur.ID, "procore_id", rfi.ID, "error", err)
		resp := map[string]any{"recovered": true, "error": err.Error(), "rfi": rfi}
		merr := d.markSubmitted(wctx, cur, Result{ProcoreID: rfi.ID, Number: rfi.Number, URL: rfi.Link}, request, resp, ms)
		if !errors.Is(merr, errRecordOwned) {
			return merr
		}
		// Another draft adopted it first: no match; retry.
	case n > 1:
		return d.failAmbiguous(wctx, cur, n, request, ms)
	}
	d.auditAttemptFailed(wctx, cur, request, err, ms, true)
	return err
}

// definitiveStatus reports whether a Procore error status means the request
// was refused and nothing was created: any 4xx except 408 (timeout) and 429
// (rate limit), which may succeed on a retry.
func definitiveStatus(status int) bool {
	return status >= 400 && status < 500 && status != http.StatusRequestTimeout && status != http.StatusTooManyRequests
}

// maxRejectionBody caps how much of a Procore error body a draft shows.
const maxRejectionBody = 200

// rejectionMessage is the _form message for a refused POST: the status and
// the start of Procore's body. The full body stays in the audit row.
func rejectionMessage(api *procore.APIError) string {
	body := strings.TrimSpace(api.Body)
	if r := []rune(body); len(r) > maxRejectionBody {
		body = string(r[:maxRejectionBody]) + "…"
	}
	if body == "" {
		return fmt.Sprintf("Procore refused the RFI (HTTP %d).", api.Status)
	}
	return fmt.Sprintf("Procore refused the RFI (HTTP %d): %s", api.Status, body)
}

// failAmbiguous fails the draft because n RFIs in Procore could be its own.
// No retry can tell them apart, so the user is told to check Procore, and
// the failure closes the unknown run: the user's next submit posts.
func (d *Drafts) failAmbiguous(ctx context.Context, cur Draft, n int, request json.RawMessage, ms int) error {
	d.log.Warn("several rfis could be this draft's; adopting none", "draft_id", cur.ID, "matches", n)
	return d.failSubmit(ctx, cur, submitFailure{
		code:    CodeProcoreValidation,
		message: AmbiguousRFIMessage,
		cause:   fmt.Errorf("pipeline: %d rfis could be this draft's", n),
		invalid: []Unresolved{{Field: FormField, Reason: ReasonProcoreValidation, Message: AmbiguousRFIMessage, Candidates: []LookupRow{}}},
		request: request, ms: ms,
		closesRun: true,
	})
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
	return d.failSubmit(ctx, cur, submitFailure{code: CodeProcoreUnavailable, message: UnavailableMessage, cause: cause})
}

// findCreated looks in the project's RFIs for ones that could be this
// draft's: a subject exactly one of subjects, created by the draft user's
// Procore account (creatorID), at or after since less recoveryWindow, and
// not already stored by another draft. It returns the match and how many
// there are; only exactly one may be adopted. A list or store error is
// returned as is.
func (d *Drafts) findCreated(ctx context.Context, token string, cur Draft, creatorID int64, subjects []string, since time.Time) (procore.RFI, int, error) {
	rfis, err := d.procore.ListRFIs(ctx, token, cur.CompanyID, cur.ProjectID)
	if err != nil {
		return procore.RFI{}, 0, err
	}
	from := since.Add(-recoveryWindow)
	var match procore.RFI
	n := 0
	for _, r := range rfis {
		if !slices.Contains(subjects, r.Subject) || r.CreatedBy.ID != creatorID {
			continue
		}
		at, err := time.Parse(time.RFC3339, r.CreatedAt)
		if err != nil || at.Before(from) {
			continue
		}
		owner, err := d.st.Q.OtherDraftByProcoreRecord(ctx, db.OtherDraftByProcoreRecordParams{
			CompanyID: cur.CompanyID, ProjectID: cur.ProjectID,
			ProcoreRecordID: pgtype.Int8{Int64: r.ID, Valid: true},
			ID:              pgtype.UUID{Bytes: cur.ID, Valid: true},
		})
		switch {
		case err == nil:
			d.log.Info("rfi belongs to another draft; not adopting", "draft_id", cur.ID, "procore_id", r.ID, "owner", uuid.UUID(owner.Bytes))
			continue
		case !errors.Is(err, pgx.ErrNoRows):
			return procore.RFI{}, 0, fmt.Errorf("pipeline: check rfi owner: %w", err)
		}
		match = r
		n++
	}
	return match, n, nil
}

// errRecordOwned means markSubmitted found the Procore record already
// stored by another draft (drafts_procore_record_uidx); nothing was stored.
var errRecordOwned = errors.New("pipeline: procore record is stored by another draft")

// procoreRecordIndex is the unique index that gives a Procore record one
// draft.
const procoreRecordIndex = "drafts_procore_record_uidx"

// markSubmitted stores the Procore record and a submitted audit event. When
// another draft stored the same record first, nothing is stored and
// errRecordOwned is returned.
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
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23505" && pgErr.ConstraintName == procoreRecordIndex {
		d.log.Info("rfi stored by another draft first; not adopting", "draft_id", cur.ID, "procore_id", res.ProcoreID)
		return errRecordOwned
	}
	if err != nil {
		d.log.Error("store submitted draft", "draft_id", cur.ID, "procore_id", res.ProcoreID, "error", err)
	}
	return err
}

// submitFailure is a definitive submit failure for failSubmit.
type submitFailure struct {
	// code and message are stored on the draft; message is user-facing.
	code, message string
	// cause goes to the log and the audit event only.
	cause error
	// invalid replaces earlier unresolved entries for the same fields.
	invalid []Unresolved
	// request and ms describe the POST, when there was one.
	request json.RawMessage
	ms      int
	// rejected means Procore answered this attempt's POST with a refusal,
	// so that attempt created nothing.
	rejected bool
	// closesRun means the user has been told an earlier attempt's RFI may
	// exist; RunSubmit stops looking for it.
	closesRun bool
}

// failedAudit is the procore_response of a failed audit event. RunSubmit
// reads Rejected and ClosesRun back in priorAttempts; the rest is for
// people reading the trail.
type failedAudit struct {
	Error  string       `json:"error"`
	Cause  string       `json:"cause,omitempty"`
	Fields []Unresolved `json:"fields,omitempty"`
	// UnknownOutcome: this attempt's POST may have created an RFI.
	UnknownOutcome bool `json:"unknown_outcome,omitempty"`
	// BeforePost: the attempt failed before posting anything.
	BeforePost bool `json:"before_post,omitempty"`
	Rejected   bool `json:"rejected,omitempty"`
	ClosesRun  bool `json:"closes_unknown_run,omitempty"`
}

// failSubmit marks the draft failed with fail's code and message and writes
// a failed audit event. Entries in fail.invalid replace earlier unresolved
// entries for the same field; the rest are kept. It returns nil once
// stored, so the job is not retried, or the store error.
func (d *Drafts) failSubmit(ctx context.Context, cur Draft, fail submitFailure) error {
	unresolved := append([]Unresolved{}, fail.invalid...)
	replaced := map[string]bool{}
	for _, u := range fail.invalid {
		replaced[u.Field] = true
	}
	for _, u := range cur.Unresolved {
		if !replaced[u.Field] {
			unresolved = append(unresolved, u)
		}
	}
	// procore_validation entries always block; a nil feature (unknown name)
	// marks by reason only.
	unresolved = markBlocking(d.features[cur.Feature], unresolved)
	unresolvedJSON, err := json.Marshal(unresolved)
	if err != nil {
		return fmt.Errorf("pipeline: encode unresolved: %w", err)
	}
	response := failedAudit{
		Error:          fail.message,
		Fields:         fail.invalid,
		UnknownOutcome: fail.code == CodeProcoreUnavailable,
		Rejected:       fail.rejected,
		ClosesRun:      fail.closesRun,
	}
	if fail.cause != nil {
		response.Cause = fail.cause.Error()
	}
	err = d.st.WithTx(ctx, func(q *db.Queries) error {
		if err := q.SetDraftFailed(ctx, db.SetDraftFailedParams{
			ID:           pgtype.UUID{Bytes: cur.ID, Valid: true},
			ErrorCode:    pgtype.Text{String: fail.code, Valid: true},
			ErrorMessage: pgtype.Text{String: fail.message, Valid: true},
			Unresolved:   unresolvedJSON,
		}); err != nil {
			return fmt.Errorf("pipeline: mark failed: %w", err)
		}
		return insertProcoreAudit(ctx, q, cur.CompanyID, cur.ID, AuditFailed, fail.request, response, fail.ms)
	})
	if err != nil {
		d.log.Error("store failed draft", "draft_id", cur.ID, "code", fail.code, "error", err)
		return err
	}
	d.log.Info("draft submit failed", "draft_id", cur.ID, "code", fail.code, "cause", fail.cause)
	return nil
}

// attemptAudit is the payload of an attempt audit event, written just before
// a POST. Subject is the RFI subject posted, so a later attempt can find
// this one's RFI even after the subject was edited. Markers written before
// Subject existed have none; the current subject stands in for them.
type attemptAudit struct {
	Attempt int       `json:"attempt"`
	At      time.Time `json:"at"`
	Subject string    `json:"subject,omitempty"`
}

// writeAttempt records that attempt n is about to post subject. An error
// stops the attempt before the POST.
func (d *Drafts) writeAttempt(ctx context.Context, cur Draft, n int, subject string, at time.Time) error {
	if err := insertAudit(ctx, d.st.Q, cur.CompanyID, cur.ID, AuditAttempt, attemptAudit{Attempt: n, At: at, Subject: subject}, 0); err != nil {
		return fmt.Errorf("pipeline: write submit attempt marker: %w", err)
	}
	return nil
}

// attemptHistory summarises a draft's audit trail for RunSubmit.
type attemptHistory struct {
	// attempts counts every attempt marker the draft has.
	attempts int
	// unknown is true when the current run holds an attempt whose POST may
	// have created an RFI; since is when the earliest such attempt began
	// and subjects are the subjects those attempts posted.
	unknown  bool
	since    time.Time
	subjects []string
}

// priorAttempts reads the draft's audit trail, newest first. The current
// run reaches back to the last submitted event, or the last failure that
// told the user to check Procore (closes_unknown_run). Inside it, every
// attempt marker is a POST that may have created an RFI, unless the event
// right after it is a rejection of that POST (a 4xx such as a 422). Edits
// and other failures do not end the run: an edit says nothing about what
// an earlier POST did. Failures before any POST have no marker and so never
// make the run unknown.
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
	rejectedAfter := false
	for i := len(evs) - 1; i >= 0; i-- {
		ev := evs[i]
		resolved := rejectedAfter
		rejectedAfter = false
		switch ev.Kind {
		case AuditSubmitted:
			return h, nil
		case AuditFailed:
			var r failedAudit
			if json.Unmarshal(ev.ProcoreResponse, &r) == nil {
				if r.ClosesRun {
					return h, nil
				}
				rejectedAfter = r.Rejected
			}
		case AuditAttempt:
			if resolved {
				continue // Procore refused this POST; it created nothing
			}
			var a attemptAudit
			if json.Unmarshal(ev.Payload, &a) != nil || a.At.IsZero() {
				a.At = ev.CreatedAt.Time
			}
			if !h.unknown || a.At.Before(h.since) {
				h.since = a.At
			}
			h.unknown = true
			if a.Subject != "" && !slices.Contains(h.subjects, a.Subject) {
				h.subjects = append(h.subjects, a.Subject)
			}
		}
	}
	return h, nil
}

// auditAttemptFailed records an attempt that will be retried. The draft
// stays submitting. posted says whether the attempt reached the POST, so
// its outcome is unknown. A store error is logged, not returned, so the
// attempt's own error reaches River.
func (d *Drafts) auditAttemptFailed(ctx context.Context, cur Draft, request json.RawMessage, cause error, ms int, posted bool) {
	resp := failedAudit{Error: cause.Error(), UnknownOutcome: posted, BeforePost: !posted}
	if err := insertProcoreAudit(ctx, d.st.Q, cur.CompanyID, cur.ID, AuditFailed, request, resp, ms); err != nil {
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
			v, ok := ParseAmount(s)
			if !ok {
				invalid = append(invalid, Unresolved{
					Field: field, Reason: ReasonInvalidValue, Message: ImpactValueMessage, Candidates: []LookupRow{},
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
