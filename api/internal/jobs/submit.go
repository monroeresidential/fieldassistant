package jobs

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"

	"github.com/google/uuid"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/rivertype"
)

// KindDraftSubmit is the River kind of the submit job.
const KindDraftSubmit = "draft_submit"

// SubmitArgs are the submit job's arguments. Jobs are unique by these args
// while active, so one submit attempt of a draft has at most one active job.
// Attempt is the draft's submit attempt number (drafts.submit_attempts), so
// a resubmit gets a new job even while River still shows the previous
// attempt's job running.
type SubmitArgs struct {
	DraftID uuid.UUID `json:"draft_id"`
	Attempt int       `json:"attempt"`
}

// Kind implements river.JobArgs.
func (SubmitArgs) Kind() string { return KindDraftSubmit }

// Submitter posts drafts to Procore. *pipeline.Drafts satisfies it.
type Submitter interface {
	// RunSubmit posts the draft; an error means the outcome is unknown and
	// the job should be retried.
	RunSubmit(ctx context.Context, draftID uuid.UUID) error
	// FailSubmit marks the draft failed once its job is out of attempts.
	FailSubmit(ctx context.Context, draftID uuid.UUID, cause error) error
}

// SubmitWorker works submit jobs by calling RunSubmit.
type SubmitWorker struct {
	river.WorkerDefaults[SubmitArgs]
	Submitter Submitter
}

// Work implements river.Worker.
func (w *SubmitWorker) Work(ctx context.Context, job *river.Job[SubmitArgs]) error {
	return w.Submitter.RunSubmit(ctx, job.Args.DraftID)
}

// errorHandler logs failed attempts and, on a submit job's last attempt,
// marks its draft failed so the user is not left waiting on a discarded job.
type errorHandler struct {
	s   Submitter
	log *slog.Logger
}

func (h *errorHandler) HandleError(ctx context.Context, job *rivertype.JobRow, err error) *river.ErrorHandlerResult {
	h.handle(ctx, job, err)
	return nil
}

func (h *errorHandler) HandlePanic(ctx context.Context, job *rivertype.JobRow, panicVal any, trace string) *river.ErrorHandlerResult {
	h.log.Error("job panicked", "job_id", job.ID, "kind", job.Kind, "panic", panicVal, "trace", trace)
	h.handle(ctx, job, fmt.Errorf("panic: %v", panicVal))
	return nil
}

func (h *errorHandler) handle(ctx context.Context, job *rivertype.JobRow, err error) {
	final := job.Attempt >= job.MaxAttempts
	h.log.Warn("job attempt failed", "job_id", job.ID, "kind", job.Kind, "attempt", job.Attempt, "max_attempts", job.MaxAttempts, "final", final, "error", err)
	if !final || job.Kind != KindDraftSubmit {
		return
	}
	var args SubmitArgs
	if derr := json.Unmarshal(job.EncodedArgs, &args); derr != nil {
		h.log.Error("decode submit job args", "job_id", job.ID, "error", derr)
		return
	}
	if ferr := h.s.FailSubmit(ctx, args.DraftID, err); ferr != nil {
		h.log.Error("mark draft failed after last submit attempt", "job_id", job.ID, "draft_id", args.DraftID, "error", ferr)
	}
}
