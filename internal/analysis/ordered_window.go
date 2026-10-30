package analysis

import (
	"context"
	"fmt"
	"sync"

	"github.com/example/mutation-judge/internal/model"
)

// orderedWindow executes at most 4*workers outstanding mutations at a time.
// Unlike fixed-size batches, completed prefixes open slots immediately, so
// fast workers can start later mutations while an earlier slow worker finishes.
// Only the coordinator reads candidates and writes results; neither discovery
// order nor report order depends on worker completion order.
func orderedWindow(
	ctx context.Context,
	count, workers int,
	next func(int) (model.Mutation, error),
	progress func(int, model.Mutation),
	run func(context.Context, model.Mutation) (model.Result, bool, error, error),
	record func(model.Result, bool, error) error,
) (bool, error) {
	if count == 0 {
		return ctx.Err() == nil, nil
	}
	if count < 0 || workers < 1 {
		return false, fmt.Errorf("invalid ordered window: mutants=%d workers=%d", count, workers)
	}
	if workers > count {
		workers = count
	}
	window := 4 * workers
	if window > count {
		window = count
	}

	type job struct {
		index int
		mut   model.Mutation
	}
	type outcome struct {
		index    int
		result   model.Result
		executed bool
		cacheErr error
		err      error
		present  bool
	}
	inner, cancel := context.WithCancel(ctx)
	defer cancel()
	jobs := make(chan job, window)
	outcomes := make(chan outcome, window)
	pending := make([]outcome, window)
	var wg sync.WaitGroup
	var progressMu sync.Mutex
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for item := range jobs {
				if inner.Err() != nil {
					continue
				}
				if progress != nil {
					progressMu.Lock()
					progress(item.index, item.mut)
					progressMu.Unlock()
				}
				r, executed, cacheErr, err := run(inner, item.mut)
				// With at most `window` outstanding jobs and a `window`-sized
				// channel this send cannot block, even after cancellation.
				outcomes <- outcome{index: item.index, result: r, executed: executed, cacheErr: cacheErr, err: err, present: true}
				if err != nil {
					cancel()
				}
			}
		}()
	}

	queued, committed := 0, 0
	var firstErr error
	active := true
	for active && committed < count {
		// Window capacity is measured from the last *persisted* result,
		// not the number of completed worker calls. This bounds all
		// buffered results even when the oldest mutation is very slow.
		for queued < count && queued-committed < window && inner.Err() == nil {
			mut, err := next(queued)
			if err != nil {
				firstErr = fmt.Errorf("read mutation %d: %w", queued, err)
				cancel()
				active = false
				break
			}
			jobs <- job{index: queued, mut: mut}
			queued++
		}
		if !active || queued == committed {
			break
		}
		select {
		case item := <-outcomes:
			if item.err != nil {
				firstErr = item.err
				cancel()
				active = false
				break
			}
			if item.index < committed || item.index >= queued || pending[item.index%window].present {
				firstErr = fmt.Errorf("duplicate or out-of-window result %d", item.index)
				cancel()
				active = false
				break
			}
			pending[item.index%window] = item
			for pending[committed%window].present {
				finished := pending[committed%window]
				pending[committed%window] = outcome{} // release large result buffers promptly
				if err := record(finished.result, finished.executed, finished.cacheErr); err != nil {
					firstErr = fmt.Errorf("persist mutation result: %w", err)
					cancel()
					active = false
					break
				}
				committed++
			}
		case <-inner.Done():
			active = false
		}
	}
	close(jobs)
	cancel()
	wg.Wait()
	// A hard worker error may have canceled the context before select
	// received its outcome; do not mistakenly classify that as a normal
	// partial report. All workers have exited, so this drain is finite.
	for {
		select {
		case item := <-outcomes:
			if item.err != nil && firstErr == nil {
				firstErr = item.err
			}
		default:
			if firstErr != nil {
				return false, firstErr
			}
			return ctx.Err() == nil && committed == count, nil
		}
	}
}
