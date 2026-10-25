package jobs

import (
	"context"
	"testing"

	"github.com/riverqueue/river"
	"github.com/riverqueue/river/rivertype"
)

type fakeWarmer struct{ calls int }

func (f *fakeWarmer) WarmAll(context.Context) error { f.calls++; return nil }

func TestRiverConfig_HasWarmPeriodicJob(t *testing.T) {
	cfg := riverConfig(river.NewWorkers(), newFakeSubmitter(), &fakeWarmer{}, quiet)
	if len(cfg.PeriodicJobs) != 1 {
		t.Fatalf("periodic jobs = %d, want 1", len(cfg.PeriodicJobs))
	}
	args, opts := warmConstructor()
	if _, ok := args.(WarmRecordsArgs); !ok || args.Kind() != "records_warm" {
		t.Fatalf("args = %#v, want WarmRecordsArgs", args)
	}
	if opts == nil || !opts.UniqueOpts.ByArgs {
		t.Fatalf("opts = %+v, want unique by args", opts)
	}
	got := map[rivertype.JobState]bool{}
	for _, s := range opts.UniqueOpts.ByState {
		got[s] = true
	}
	for _, s := range []rivertype.JobState{rivertype.JobStateAvailable, rivertype.JobStateRunning, rivertype.JobStateScheduled, rivertype.JobStateRetryable} {
		if !got[s] {
			t.Errorf("unique states missing %s", s)
		}
	}
}

func TestWarmRecordsWorker_CallsWarmAll(t *testing.T) {
	w := &fakeWarmer{}
	worker := &WarmRecordsWorker{Warmer: w}
	if err := worker.Work(context.Background(), &river.Job[WarmRecordsArgs]{}); err != nil {
		t.Fatalf("Work: %v", err)
	}
	if w.calls != 1 {
		t.Errorf("WarmAll calls = %d, want 1", w.calls)
	}
}
