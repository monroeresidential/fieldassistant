package jobs

import (
	"context"
	"time"

	"github.com/riverqueue/river"
	"github.com/riverqueue/river/rivertype"
)

// KindRecordsWarm is the River kind of the keep-warm job.
const KindRecordsWarm = "records_warm"

// WarmInterval is how often the keep-warm job runs.
const WarmInterval = 15 * time.Minute

// warmTimeout bounds one warm run. WarmAll bounds each user and project pair
// at 30 s on its own, so this only caps a long list of pairs; pairs not
// reached wait for the next tick. It stays under RescueStuckJobsAfter so a
// live run is never rescued.
const warmTimeout = 4 * time.Minute

// Warmer refreshes the record mirrors of recently synced projects.
// *records.Mirror satisfies it.
type Warmer interface {
	WarmAll(ctx context.Context) error
}

// WarmRecordsArgs are the keep-warm job's arguments; the job takes none.
type WarmRecordsArgs struct{}

// Kind implements river.JobArgs.
func (WarmRecordsArgs) Kind() string { return KindRecordsWarm }

// WarmRecordsWorker works keep-warm jobs by calling WarmAll.
type WarmRecordsWorker struct {
	river.WorkerDefaults[WarmRecordsArgs]
	Warmer Warmer
}

// Timeout implements river.Worker.
func (w *WarmRecordsWorker) Timeout(*river.Job[WarmRecordsArgs]) time.Duration { return warmTimeout }

// Work implements river.Worker.
func (w *WarmRecordsWorker) Work(ctx context.Context, _ *river.Job[WarmRecordsArgs]) error {
	return w.Warmer.WarmAll(ctx)
}

// warmConstructor builds each periodic insert. The job is unique across its
// active states, so a slow run never stacks up behind itself. Completed is
// left out so a finished run never blocks a later tick. A warm run never
// retries; the next tick enqueues a fresh one.
func warmConstructor() (river.JobArgs, *river.InsertOpts) {
	return WarmRecordsArgs{}, &river.InsertOpts{MaxAttempts: 1, UniqueOpts: river.UniqueOpts{
		ByArgs: true,
		ByState: []rivertype.JobState{
			rivertype.JobStateAvailable,
			rivertype.JobStatePending,
			rivertype.JobStateRunning,
			rivertype.JobStateScheduled,
			rivertype.JobStateRetryable,
		},
	}}
}

// warmPeriodicJob schedules the keep-warm job every WarmInterval.
func warmPeriodicJob() *river.PeriodicJob {
	return river.NewPeriodicJob(river.PeriodicInterval(WarmInterval), warmConstructor,
		&river.PeriodicJobOpts{RunOnStart: false})
}
