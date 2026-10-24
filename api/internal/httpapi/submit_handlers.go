package httpapi

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/google/uuid"

	"github.com/monroeresidential/fieldassistant/api/internal/jobs"
	"github.com/monroeresidential/fieldassistant/api/internal/pipeline"
)

var errJobNotFound = &APIError{Status: http.StatusNotFound, Code: "not_found", Message: "job not found"}

// SubmitDraft queues the user's draft for posting to Procore. The
// Idempotency-Key must be the draft id. An already submitted draft answers
// 200; a newly queued or in-flight submit answers 202 with its job.
func (s *Server) SubmitDraft(ctx context.Context, req SubmitDraftRequestObject) (SubmitDraftResponseObject, error) {
	if key, err := uuid.Parse(req.Params.IdempotencyKey); err != nil || key != req.DraftId {
		return nil, badRequest("Idempotency-Key must equal the draft id")
	}
	if _, err := s.companyDraft(ctx, req.Params.XCompanyId, req.DraftId); err != nil {
		return nil, err
	}
	enqueue := func(ctx context.Context, draftID uuid.UUID) (int64, error) {
		return jobs.Enqueue(ctx, s.deps.Jobs, draftID)
	}
	out, err := s.deps.Drafts.Submit(ctx, claimsFrom(ctx).UserID, req.DraftId, enqueue)
	var unresolved *pipeline.ErrUnresolved
	if errors.As(err, &unresolved) {
		return nil, badRequest("draft has unresolved fields: " + strings.Join(unresolved.Fields, ", "))
	}
	if err != nil {
		return nil, s.draftFailure("submit draft", err)
	}
	body := SubmitResponse{DraftId: req.DraftId}
	if out.JobID != 0 {
		body.JobId = strconv.FormatInt(out.JobID, 10)
	}
	if out.AlreadySubmitted {
		body.Status = SubmitResponseStatusSubmitted
		return SubmitDraft200JSONResponse(body), nil
	}
	body.Status = SubmitResponseStatusSubmitting
	return SubmitDraft202JSONResponse(body), nil
}

// GetJob reports a submit job of one of the user's drafts in the company.
// When it is the draft's current job, a finished job takes its outcome from
// the draft: a job whose draft failed (a Procore 422, a lost sign-in) is
// failed even though River completed it, and result and error come from
// the draft. Any other job reports River's state alone.
func (s *Server) GetJob(ctx context.Context, req GetJobRequestObject) (GetJobResponseObject, error) {
	jobID, err := strconv.ParseInt(req.JobId, 10, 64)
	if err != nil {
		return nil, errJobNotFound
	}
	js, err := jobs.Status(ctx, s.deps.Jobs, jobID)
	if errors.Is(err, jobs.ErrNotFound) {
		return nil, errJobNotFound
	}
	if err != nil {
		return nil, err
	}
	d, err := s.companyDraft(ctx, req.Params.XCompanyId, js.DraftID)
	if errors.Is(err, errDraftNotFound) {
		return nil, errJobNotFound
	}
	if err != nil {
		return nil, err
	}

	out := JobStatus{Id: req.JobId, DraftId: d.ID, Status: JobStatusStatus(js.Status)}
	if d.JobID != jobID {
		// An earlier submission's job, or one whose ID the draft has not
		// stored yet: the draft's outcome is not this job's.
		return GetJob200JSONResponse(out), nil
	}
	if js.Status == jobs.StatusSucceeded && d.Status == pipeline.StatusFailed {
		out.Status = JobStatusStatusFailed
	}
	dr, err := draftResponse(d)
	if err != nil {
		return nil, err
	}
	switch out.Status {
	case JobStatusStatusSucceeded:
		out.Result = dr.Result
	case JobStatusStatusFailed:
		out.Error = dr.Error
	}
	return GetJob200JSONResponse(out), nil
}
