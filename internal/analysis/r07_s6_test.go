package analysis

import (
	"context"
	"github.com/example/mutation-judge/internal/config"
	"github.com/example/mutation-judge/internal/runner"
	"testing"
)

func TestR07S6UnlimitedDiscoveryUsesDiskAndPreservesBounds(t *testing.T) {
	dir, verdicts := fourMutantFixture(t)
	cfg := config.Default()
	cfg.Cache = false
	cfg.Operators = []string{"boundary"}
	cfg.MaxMutants = 0
	toolchain, err := runner.DetectToolchain(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := (Engine{}).prepareContextMode(context.Background(), Request{CWD: dir, Patterns: []string{"."}, Config: cfg}, toolchain, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(prepared.mutants) != 0 || prepared.mutationSpool == nil || prepared.mutantCount() != 4 {
		t.Fatalf("unbounded candidates in memory: %d, spool=%v, count=%d", len(prepared.mutants), prepared.mutationSpool != nil, prepared.mutantCount())
	}
	_ = prepared.mutationSpool.Close()
	prepared.cleanup()
	req := Request{CWD: dir, Patterns: []string{"."}, Config: cfg}
	for _, workers := range []int{1, 3} {
		cfg.Workers = workers
		req.Config = cfg
		original, err := (Engine{Backend: &contentAwareFakeBackend{relPath: "p.go", verdicts: verdicts}}).Analyze(context.Background(), req)
		if err != nil {
			t.Fatal(err)
		}
		streamed, spool, err := (Engine{Backend: &contentAwareFakeBackend{relPath: "p.go", verdicts: verdicts}}).AnalyzeSpool(context.Background(), req)
		if err != nil {
			t.Fatal(err)
		}
		if streamed.Bounds["retained_after_bound"] != 4 || streamed.Bounds["discovered_before_bound"] != 4 || streamed.Summary != original.Summary {
			t.Fatalf("worker %d inconsistent metadata: %+v", workers, streamed.Bounds)
		}
		if spool.Len() != len(original.Results) {
			t.Fatalf("worker %d records %d", workers, spool.Len())
		}
		_ = spool.Close()
	}
}
