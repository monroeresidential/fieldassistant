// Package recordstest provides a scripted records fetcher for tests.
package recordstest

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/monroeresidential/fieldassistant/api/internal/procore"
)

// ErrScriptExhausted is returned when ListSince is called more times than
// Script has entries.
var ErrScriptExhausted = errors.New("recordstest: fetcher script exhausted")

// FakeFetcher satisfies records.Fetcher. Each ListSince call consumes the
// next Script entry and records its since argument in Calls. When Block is
// non-nil, ListSince waits for it to be closed (or the context to end)
// before returning.
type FakeFetcher struct {
	Script []func(since *time.Time) ([]procore.RawRecord, error)
	Calls  []*time.Time
	Block  chan struct{}

	mu sync.Mutex
}

// ListSince records the call, then returns the next scripted result.
func (f *FakeFetcher) ListSince(ctx context.Context, _ string, _, _ int64, since *time.Time) ([]procore.RawRecord, error) {
	f.mu.Lock()
	var s *time.Time
	if since != nil {
		v := *since
		s = &v
	}
	f.Calls = append(f.Calls, s)
	var step func(since *time.Time) ([]procore.RawRecord, error)
	if len(f.Script) > 0 {
		step, f.Script = f.Script[0], f.Script[1:]
	}
	block := f.Block
	f.mu.Unlock()

	if block != nil {
		select {
		case <-block:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	if step == nil {
		return nil, ErrScriptExhausted
	}
	return step(since)
}

// CallCount returns how many times ListSince has been called. It is safe to
// call while ListSince runs on another goroutine.
func (f *FakeFetcher) CallCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.Calls)
}

// Returns is a Script entry that ignores since and returns recs.
func Returns(recs ...procore.RawRecord) func(*time.Time) ([]procore.RawRecord, error) {
	return func(*time.Time) ([]procore.RawRecord, error) { return recs, nil }
}

// Fails is a Script entry that returns err.
func Fails(err error) func(*time.Time) ([]procore.RawRecord, error) {
	return func(*time.Time) ([]procore.RawRecord, error) { return nil, err }
}
