// Package jobs runs background work on a River queue in Postgres: today the
// draft submit job.
package jobs

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/riverdriver/riverpgxv5"
	"github.com/riverqueue/river/rivertype"
)

// MaxAttempts is how many times a submit job runs before River discards it.
// Retries follow River's default backoff.
const MaxAttempts = 4

// Job statuses, matching the contract's JobStatus.status enum.
const (
	StatusQueued    = "queued"
	StatusRunning   = "running"
	StatusSucceeded = "succeeded"
	StatusFailed    = "failed"
)

// ErrNotFound means no submit job has the ID.
var ErrNotFound = errors.New("jobs: job not found")

// NewClient builds a River client over pool with one default queue of four
// workers running submit jobs through s. Call Start to begin working jobs
// and Stop on shutdown; an unstarted client can still insert and read jobs.
// A nil logger means slog.Default().
func NewClient(pool *pgxpool.Pool, s Submitter, logger *slog.Logger) (*river.Client[pgx.Tx], error) {
	if logger == nil {
		logger = slog.Default()
	}
	workers := river.NewWorkers()
	if err := river.AddWorkerSafely(workers, &SubmitWorker{Submitter: s}); err != nil {
		return nil, fmt.Errorf("jobs: add submit worker: %w", err)
	}
	c, err := river.NewClient(riverpgxv5.New(pool), &river.Config{
		Queues:       map[string]river.QueueConfig{river.QueueDefault: {MaxWorkers: 4}},
		Workers:      workers,
		Logger:       logger,
		ErrorHandler: &errorHandler{s: s, log: logger},
	})
	if err != nil {
		return nil, fmt.Errorf("jobs: river client: %w", err)
	}
	return c, nil
}

// Enqueue inserts a submit job for draftID and returns its ID. While a job
// for the draft is pending, queued, running or waiting to retry, River skips
// the insert and the existing job's ID is returned, so a draft never has two
// submit jobs in flight.
func Enqueue(ctx context.Context, c *river.Client[pgx.Tx], draftID uuid.UUID) (int64, error) {
	res, err := c.Insert(ctx, SubmitArgs{DraftID: draftID}, &river.InsertOpts{
		MaxAttempts: MaxAttempts,
		UniqueOpts: river.UniqueOpts{
			ByArgs: true,
			// River requires pending alongside the four active states.
			ByState: []rivertype.JobState{
				rivertype.JobStateAvailable,
				rivertype.JobStatePending,
				rivertype.JobStateRetryable,
				rivertype.JobStateRunning,
				rivertype.JobStateScheduled,
			},
		},
	})
	if err != nil {
		return 0, fmt.Errorf("jobs: insert submit job: %w", err)
	}
	return res.Job.ID, nil
}

// JobState is a submit job's status and the draft it submits.
type JobState struct {
	ID      int64
	Status  string
	DraftID uuid.UUID
}

// Status returns the submit job with jobID. A missing job, or a job of
// another kind, is ErrNotFound.
func Status(ctx context.Context, c *river.Client[pgx.Tx], jobID int64) (JobState, error) {
	job, err := c.JobGet(ctx, jobID)
	if errors.Is(err, river.ErrNotFound) {
		return JobState{}, ErrNotFound
	}
	if err != nil {
		return JobState{}, fmt.Errorf("jobs: get job: %w", err)
	}
	if job.Kind != KindDraftSubmit {
		return JobState{}, ErrNotFound
	}
	var args SubmitArgs
	if err := json.Unmarshal(job.EncodedArgs, &args); err != nil {
		return JobState{}, fmt.Errorf("jobs: decode job %d args: %w", jobID, err)
	}
	return JobState{ID: job.ID, Status: statusFor(job.State), DraftID: args.DraftID}, nil
}

// statusFor maps a River job state onto the contract's job status.
func statusFor(s rivertype.JobState) string {
	switch s {
	case rivertype.JobStateRunning:
		return StatusRunning
	case rivertype.JobStateCompleted:
		return StatusSucceeded
	case rivertype.JobStateDiscarded, rivertype.JobStateCancelled:
		return StatusFailed
	default: // available, scheduled, retryable, pending
		return StatusQueued
	}
}
