// Package jobs runs background work on a River queue in Postgres: today the
// draft submit job.
package jobs

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

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

const (
	// JobTimeout bounds one run of a job. A submit pushes up to ten photos
	// at up to 90 s each (creating the upload, then a storage POST of at
	// most 60 s) before a few Procore calls of at most 30 s each.
	JobTimeout = 15 * time.Minute
	// RescueStuckJobsAfter is how long a job may stay running before River
	// assumes its process died (a deploy killed it mid-run) and retries it.
	// It must exceed JobTimeout so a live job is never rescued: a submit
	// rescued mid-push would push its photos twice at once.
	RescueStuckJobsAfter = 20 * time.Minute
	// JobRetention is how long River keeps completed and discarded jobs, so
	// a client polling a job after a long background stretch still finds it.
	// GetJob answers from the draft once the row is gone.
	JobRetention = 7 * 24 * time.Hour
)

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
// workers running submit jobs through s and a periodic job that keeps record
// mirrors warm through w. Call Start to begin working jobs
// and Stop on shutdown; an unstarted client can still insert and read jobs.
// A nil logger means slog.Default().
func NewClient(pool *pgxpool.Pool, s Submitter, w Warmer, logger *slog.Logger) (*river.Client[pgx.Tx], error) {
	if logger == nil {
		logger = slog.Default()
	}
	workers := river.NewWorkers()
	if err := river.AddWorkerSafely(workers, &SubmitWorker{Submitter: s}); err != nil {
		return nil, fmt.Errorf("jobs: add submit worker: %w", err)
	}
	if err := river.AddWorkerSafely(workers, &WarmRecordsWorker{Warmer: w}); err != nil {
		return nil, fmt.Errorf("jobs: add warm worker: %w", err)
	}
	c, err := river.NewClient(riverpgxv5.New(pool), riverConfig(workers, s, logger))
	if err != nil {
		return nil, fmt.Errorf("jobs: river client: %w", err)
	}
	return c, nil
}

// riverConfig is the River configuration NewClient uses.
func riverConfig(workers *river.Workers, s Submitter, logger *slog.Logger) *river.Config {
	return &river.Config{
		Queues:                      map[string]river.QueueConfig{river.QueueDefault: {MaxWorkers: 4}},
		Workers:                     workers,
		Logger:                      logger,
		ErrorHandler:                &errorHandler{s: s, log: logger},
		JobTimeout:                  JobTimeout,
		RescueStuckJobsAfter:        RescueStuckJobsAfter,
		CompletedJobRetentionPeriod: JobRetention,
		DiscardedJobRetentionPeriod: JobRetention,
		PeriodicJobs:                []*river.PeriodicJob{warmPeriodicJob()},
	}
}

// Queue inserts and inspects submit jobs. It satisfies pipeline.SubmitQueue.
type Queue struct {
	c *river.Client[pgx.Tx]
}

// NewQueue wraps a River client built by NewClient.
func NewQueue(c *river.Client[pgx.Tx]) *Queue {
	return &Queue{c: c}
}

// EnqueueTx inserts the submit job for attempt of draftID inside tx and
// returns its ID; the job exists only if tx commits. While a job for the
// same draft and attempt is pending, queued, running or waiting to retry,
// River skips the insert and the existing job's ID is returned. A new
// attempt number always gets a new job, even while the previous attempt's
// job is still finishing.
func (q *Queue) EnqueueTx(ctx context.Context, tx pgx.Tx, draftID uuid.UUID, attempt int) (int64, error) {
	res, err := q.c.InsertTx(ctx, tx, SubmitArgs{DraftID: draftID, Attempt: attempt}, &river.InsertOpts{
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

// JobFinished reports whether jobID will never run again: it completed, was
// discarded or cancelled, or River no longer has it.
func (q *Queue) JobFinished(ctx context.Context, jobID int64) (bool, error) {
	job, err := q.c.JobGet(ctx, jobID)
	if errors.Is(err, river.ErrNotFound) {
		return true, nil
	}
	if err != nil {
		return false, fmt.Errorf("jobs: get job: %w", err)
	}
	switch job.State {
	case rivertype.JobStateCompleted, rivertype.JobStateDiscarded, rivertype.JobStateCancelled:
		return true, nil
	}
	return false, nil
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
