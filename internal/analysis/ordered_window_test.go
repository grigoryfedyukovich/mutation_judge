package analysis

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/example/mutation-judge/internal/model"
)

func TestR07S7SlidingWindowDoesNotWaitForBatchBarrier(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	blocked := make(chan struct{})
	laterStarted := make(chan struct{}, 1)
	release := make(chan struct{})
	var once sync.Once
	defer once.Do(func() { close(release) })
	type output struct {
		complete bool
		err      error
		ordered  []string
	}
	finished := make(chan output, 1)
	go func() {
		var ordered []string
		complete, err := orderedWindow(ctx, 13, 2,
			func(i int) (model.Mutation, error) { return model.Mutation{ID: strconv.Itoa(i)}, nil },
			nil,
			func(ctx context.Context, m model.Mutation) (model.Result, bool, error, error) {
				i, _ := strconv.Atoi(m.ID)
				if i == 7 {
					close(blocked)
					select {
					case <-release:
					case <-ctx.Done():
					}
				}
				if i == 8 {
					laterStarted <- struct{}{}
				}
				return model.Result{Mutation: m, Verdict: model.VerdictSurvived}, true, nil, nil
			},
			func(r model.Result, _ bool, _ error) error {
				ordered = append(ordered, r.Mutation.ID)
				return nil
			})
		finished <- output{complete, err, ordered}
	}()
	select {
	case <-blocked:
	case <-ctx.Done():
		t.Fatal("eighth job never started")
	}
	select {
	case <-laterStarted:
		// Job #8 ran while job #7 is still blocked. Fixed batches
		// cannot satisfy this property.
	case <-ctx.Done():
		t.Fatal("sliding window failed to replenish a blocked batch tail")
	}
	once.Do(func() { close(release) })
	got := <-finished
	if got.err != nil || !got.complete || len(got.ordered) != 13 {
		t.Fatalf("unexpected outcome: %+v", got)
	}
	for i, id := range got.ordered {
		if id != strconv.Itoa(i) {
			t.Fatalf("out-of-order result %d: %s", i, id)
		}
	}
}

func TestR07S7SlowHeadBoundsOutstandingWindow(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	release := make(chan struct{})
	var once sync.Once
	defer once.Do(func() { close(release) })
	firstStarted := make(chan struct{})
	var started atomic.Int32
	done := make(chan error, 1)
	go func() {
		complete, err := orderedWindow(ctx, 100, 4,
			func(i int) (model.Mutation, error) { return model.Mutation{ID: strconv.Itoa(i)}, nil },
			nil,
			func(ctx context.Context, m model.Mutation) (model.Result, bool, error, error) {
				started.Add(1)
				if m.ID == "0" {
					close(firstStarted)
					select {
					case <-release:
					case <-ctx.Done():
					}
				}
				return model.Result{Mutation: m, Verdict: model.VerdictKilled}, true, nil, nil
			},
			func(model.Result, bool, error) error { return nil })
		if !complete && err == nil {
			err = errors.New("unexpected incomplete run")
		}
		done <- err
	}()
	select {
	case <-firstStarted:
	case <-ctx.Done():
		t.Fatal("head was not scheduled")
	}
	// All fifteen followers may complete, but none of their results can
	// be persisted until the oldest mutation finishes.
	time.Sleep(60 * time.Millisecond)
	if n := started.Load(); n > 16 {
		t.Fatalf("scheduled %d mutations behind blocked head; bound is 16", n)
	}
	once.Do(func() { close(release) })
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if n := started.Load(); n != 100 {
		t.Fatalf("expected all mutants to execute, got %d", n)
	}
}

func TestR07S7WindowHardErrorsAndCancellation(t *testing.T) {
	for _, tc := range []struct {
		name      string
		runErr    bool
		recordErr bool
		cancel    bool
	}{
		{name: "worker error", runErr: true},
		{name: "spool error", recordErr: true},
		{name: "cancellation", cancel: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			complete, err := orderedWindow(ctx, 50, 4,
				func(i int) (model.Mutation, error) { return model.Mutation{ID: strconv.Itoa(i)}, nil },
				nil,
				func(ctx context.Context, m model.Mutation) (model.Result, bool, error, error) {
					if tc.cancel {
						cancel()
					}
					if tc.runErr {
						return model.Result{}, false, nil, errors.New("worker broke")
					}
					return model.Result{Mutation: m}, true, nil, nil
				},
				func(model.Result, bool, error) error {
					if tc.recordErr {
						return errors.New("disk full")
					}
					return nil
				})
			if complete {
				t.Fatal("error/canceled run reported complete")
			}
			if tc.cancel {
				if err != nil {
					t.Fatalf("cancellation should produce partial report: %v", err)
				}
			} else if err == nil {
				t.Fatal("lost hard error")
			}
		})
	}
}

func BenchmarkR07S7OrderedWindow(b *testing.B) {
	for _, workers := range []int{1, 4, 8} {
		b.Run(fmt.Sprintf("workers=%d", workers), func(b *testing.B) {
			b.ReportAllocs()
			for n := 0; n < b.N; n++ {
				complete, err := orderedWindow(context.Background(), 4000, workers,
					func(i int) (model.Mutation, error) { return model.Mutation{ID: strconv.Itoa(i)}, nil }, nil,
					func(_ context.Context, m model.Mutation) (model.Result, bool, error, error) {
						return model.Result{Mutation: m, Verdict: model.VerdictSurvived}, true, nil, nil
					},
					func(model.Result, bool, error) error { return nil })
				if err != nil || !complete {
					b.Fatal(err)
				}
			}
		})
	}
}
