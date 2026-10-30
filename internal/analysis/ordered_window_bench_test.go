package analysis

import (
	"context"
	"fmt"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/example/mutation-judge/internal/model"
)

// This intentionally models the prior fixed-batch barrier as an independent
// benchmark reference, not as a production execution fallback.
func benchmarkFixedBatches(count, workers int, run func(int)) {
	window := 2 * workers
	for start := 0; start < count; start += window {
		last := start + window
		if last > count {
			last = count
		}
		jobs := make(chan int)
		var wg sync.WaitGroup
		for i := 0; i < workers; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for index := range jobs {
					run(index)
				}
			}()
		}
		for i := start; i < last; i++ {
			jobs <- i
		}
		close(jobs)
		wg.Wait()
	}
}

func BenchmarkR07S7BoundarySkew(b *testing.B) {
	const count, workers = 80, 4
	// Every eighth mutation finishes substantially later than the others.
	// A fixed batch must wait for each straggler; a sliding window may
	// dispatch subsequent mutants as soon as earlier results are persisted.
	run := func(index int) {
		if index%8 == 7 {
			time.Sleep(22 * time.Millisecond)
		} else {
			time.Sleep(2 * time.Millisecond)
		}
	}
	for _, mode := range []string{"fixed_batches", "sliding_window"} {
		b.Run(mode, func(b *testing.B) {
			b.ReportAllocs()
			b.ResetTimer()
			for n := 0; n < b.N; n++ {
				if mode == "fixed_batches" {
					benchmarkFixedBatches(count, workers, run)
					continue
				}
				complete, err := orderedWindow(context.Background(), count, workers,
					func(i int) (model.Mutation, error) { return model.Mutation{ID: strconv.Itoa(i)}, nil },
					nil,
					func(_ context.Context, mut model.Mutation) (model.Result, bool, error, error) {
						i, _ := strconv.Atoi(mut.ID)
						run(i)
						return model.Result{Mutation: mut, Verdict: model.VerdictSurvived}, true, nil, nil
					},
					func(model.Result, bool, error) error { return nil },
				)
				if err != nil || !complete {
					b.Fatal(fmt.Errorf("incomplete sliding run: %v", err))
				}
			}
		})
	}
}
