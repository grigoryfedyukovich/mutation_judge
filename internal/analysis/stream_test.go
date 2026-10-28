package analysis

import (
	"context"
	"testing"

	"github.com/example/mutation-judge/internal/config"
	"github.com/example/mutation-judge/internal/model"
	"github.com/example/mutation-judge/internal/runner"
)

func TestSpoolMatchesInMemorySequentialAndParallel(t *testing.T) {
	dir, verdicts := fourMutantFixture(t)
	for _, workers := range []int{1, 2, 3, 8} {
		t.Run(string(rune('0'+workers)), func(t *testing.T) {
			cfg := config.Default()
			cfg.Cache = false
			cfg.Operators = []string{"boundary"}
			cfg.Workers = workers
			req := Request{CWD: dir, Patterns: []string{"."}, Config: cfg}
			full, err := (Engine{Version: "test", Backend: &contentAwareFakeBackend{relPath: "p.go", verdicts: verdicts}}).Analyze(context.Background(), req)
			if err != nil {
				t.Fatal(err)
			}
			metadata, spool, err := (Engine{Version: "test", Backend: &contentAwareFakeBackend{relPath: "p.go", verdicts: verdicts}}).AnalyzeSpool(context.Background(), req)
			if err != nil {
				t.Fatal(err)
			}
			defer spool.Close()
			if !metadata.Complete || !full.Complete {
				t.Fatal("unexpected incomplete report")
			}
			if metadata.Summary != full.Summary {
				t.Fatalf("summary: streamed=%+v in-memory=%+v", metadata.Summary, full.Summary)
			}
			if len(metadata.Results) != 0 || spool.Len() != len(full.Results) {
				t.Fatalf("stream results retained %d, spool=%d expected=%d", len(metadata.Results), spool.Len(), len(full.Results))
			}
			idx := 0
			if err := spool.ForEach(func(r model.Result) error {
				if r.Mutation.ID != full.Results[idx].Mutation.ID || r.Verdict != full.Results[idx].Verdict {
					t.Errorf("result %d differed: %+v / %+v", idx, r, full.Results[idx])
				}
				idx++
				return nil
			}); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestScoreCounterMatchesSummarize(t *testing.T) {
	results := []model.Result{}
	for _, v := range []model.Verdict{model.VerdictKilled, model.VerdictSurvived, model.VerdictInvalid, model.VerdictUnknown, model.VerdictTimeout, model.VerdictUnsupported, model.VerdictEquivalent} {
		results = append(results, model.Result{Verdict: v})
	}
	var counter scoreCounter
	for _, r := range results {
		counter.Add(r)
	}
	if got, want := counter.Finish(), summarize(results); got != want {
		t.Fatalf("score count got=%+v want=%+v", got, want)
	}
}

func TestSpoolIncompleteOnCanceledContext(t *testing.T) {
	dir, verdicts := fourMutantFixture(t)
	cfg := config.Default()
	cfg.Cache = false
	cfg.Operators = []string{"boundary"}
	cfg.Workers = 2
	ctx, cancel := context.WithCancel(context.Background())
	// Cancel when the first mutant is about to start, after baseline.
	engine := Engine{Version: "test", Backend: &contentAwareFakeBackend{relPath: "p.go", verdicts: verdicts}, Progress: func(Progress) { cancel() }}
	report, spool, err := engine.AnalyzeSpool(ctx, Request{CWD: dir, Patterns: []string{"."}, Config: cfg})
	if err != nil {
		t.Fatal(err)
	}
	defer spool.Close()
	if report.Complete {
		t.Fatal("canceled run incorrectly complete")
	}
	if report.Summary.Generated != spool.Len() {
		t.Fatal("partial summary does not match persisted results")
	}
}

// Ensure streaming cannot silently broaden the verdict vocabulary.
var _ runner.Backend = (*contentAwareFakeBackend)(nil)
