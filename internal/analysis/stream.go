package analysis

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"path/filepath"
	"sync"
	"time"

	"github.com/example/mutation-judge/internal/cache"
	covermap "github.com/example/mutation-judge/internal/coverage"
	"github.com/example/mutation-judge/internal/model"
	"github.com/example/mutation-judge/internal/resultspool"
)

// scoreCounter maintains exactly the same scoring rules as summarize but
// without retaining result records. Finalize is called after the final write.
type scoreCounter struct{ summary model.Summary }

func (s *scoreCounter) Add(r model.Result) {
	s.summary.Generated++
	switch r.Verdict {
	case model.VerdictKilled:
		s.summary.Killed++
	case model.VerdictSurvived:
		s.summary.Survived++
	case model.VerdictInvalid:
		s.summary.Invalid++
	case model.VerdictTimeout:
		s.summary.Timeout++
	case model.VerdictUnsupported:
		s.summary.Unsupported++
	case model.VerdictEquivalent:
		s.summary.Equivalent++
	default:
		s.summary.Unknown++
	}
}
func (s *scoreCounter) Finish() model.Summary {
	den := s.summary.Killed + s.summary.Survived
	if den == 0 {
		s.summary.ScoreText = "n/a (no scoreable mutants)"
	} else {
		s.summary.Score = 100 * float64(s.summary.Killed) / float64(den)
		s.summary.ScoreText = fmt.Sprintf("%.1f%% excluding invalid/timeout/unknown/unsupported/equivalent", s.summary.Score)
	}
	return s.summary
}

func (e Engine) executeMutantsSpool(ctx context.Context, req Request, p preparedAnalysis, coverage covermap.Map, coverageKnown bool, spool *resultspool.Spool) (int, model.Summary, bool, int64, []string, error) {
	started := time.Now()
	cfgJSON, err := json.Marshal(cacheRelevantConfig(req.Config))
	if err != nil {
		return 0, model.Summary{}, false, 0, nil, fmt.Errorf("marshal effective configuration for cache key: %w", err)
	}
	cacheDir := req.Config.CacheDir
	if !filepath.IsAbs(cacheDir) {
		cacheDir = filepath.Join(p.root, cacheDir)
	}
	store := cache.Store{Dir: cacheDir, Enabled: req.Config.Cache && p.cacheSafe}
	var score scoreCounter
	var firstCacheErr error
	writeFailures, executedCount := 0, 0
	record := func(r model.Result, executed bool, cacheErr error) error {
		if err := spool.Append(r); err != nil {
			return err
		}
		score.Add(r)
		if executed {
			executedCount++
			if cacheErr != nil {
				writeFailures++
				if firstCacheErr == nil {
					firstCacheErr = cacheErr
				}
			}
		}
		return nil
	}
	complete := ctx.Err() == nil
	workers := req.Config.Workers
	if workers < 2 {
		workers = 1
	}
	count := p.mutantCount()
	if workers > count {
		workers = count
	}
	// Batch at most 2*workers mutants. A slow early mutant cannot cause
	// unbounded later results to accumulate while waiting for ordered output.
	batchSize := workers * 2
	if batchSize < 1 {
		batchSize = 1
	}
	var nextMutation func(int) (model.Mutation, error)
	if p.mutationSpool != nil {
		reader, err := p.mutationSpool.Reader()
		if err != nil {
			return 0, model.Summary{}, false, 0, nil, err
		}
		defer reader.Close()
		nextMutation = func(int) (model.Mutation, error) { return reader.Next() }
	} else {
		nextMutation = func(i int) (model.Mutation, error) { return p.mutants[i], nil }
	}
	for start := 0; start < count; start += batchSize {
		if ctx.Err() != nil {
			complete = false
			break
		}
		end := start + batchSize
		if end > count {
			end = count
		}
		type outcome struct {
			result   model.Result
			executed bool
			cacheErr error
			err      error
			present  bool
		}
		out := make([]outcome, end-start)
		batch := make([]model.Mutation, end-start)
		for i := range batch {
			batch[i], err = nextMutation(start + i)
			if err != nil {
				return 0, model.Summary{}, false, 0, nil, fmt.Errorf("read mutation %d: %w", start+i, err)
			}
		}
		batchCtx, cancel := context.WithCancel(ctx)
		work := make(chan int)
		var wg sync.WaitGroup
		var progressMu sync.Mutex
		var errMu sync.Mutex
		var firstErr error
		for w := 0; w < workers; w++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for idx := range work {
					if batchCtx.Err() != nil {
						continue
					}
					mut := batch[idx-start]
					if e.Progress != nil {
						progressMu.Lock()
						e.Progress(Progress{Index: idx + 1, Total: count, Mutation: mut})
						progressMu.Unlock()
					}
					r, executed, cacheErr, err := e.runOneMutant(batchCtx, req, p, store, cfgJSON, mut, coverage, coverageKnown)
					if err != nil {
						errMu.Lock()
						if firstErr == nil {
							firstErr = err
						}
						errMu.Unlock()
						cancel()
						continue
					}
					out[idx-start] = outcome{result: r, executed: executed, cacheErr: cacheErr, present: true}
				}
			}()
		}
		dispatchDone := make(chan struct{})
		go func() {
			defer close(dispatchDone)
			defer close(work)
			for idx := start; idx < end; idx++ {
				select {
				case work <- idx:
				case <-batchCtx.Done():
					return
				}
			}
		}()
		wg.Wait()
		<-dispatchDone
		cancel()
		if firstErr != nil {
			return 0, model.Summary{}, false, 0, nil, firstErr
		}
		for _, item := range out {
			if !item.present {
				complete = false
				continue
			}
			if err := record(item.result, item.executed, item.cacheErr); err != nil {
				return 0, model.Summary{}, false, 0, nil, fmt.Errorf("persist mutation result: %w", err)
			}
		}
		if ctx.Err() != nil {
			complete = false
			break
		}
	}
	if ctx.Err() != nil {
		complete = false
	}
	if complete && p.mutationSpool != nil {
		if _, tailErr := nextMutation(count); tailErr != io.EOF {
			if tailErr == nil {
				tailErr = fmt.Errorf("unexpected mutation after discovery count")
			}
			return 0, model.Summary{}, false, 0, nil, fmt.Errorf("validate mutation spool: %w", tailErr)
		}
	}
	var warnings []string
	if writeFailures > 0 {
		warnings = append(warnings, fmt.Sprintf("cache write failed for %d of %d freshly executed mutant(s); those results are correct but were not cached for reuse (first error: %v)", writeFailures, executedCount, firstCacheErr))
	}
	return spool.Len(), score.Finish(), complete, time.Since(started).Milliseconds(), warnings, nil
}
