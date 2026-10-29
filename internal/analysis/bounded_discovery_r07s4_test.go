package analysis

import (
	"context"
	"testing"

	"github.com/example/mutation-judge/internal/config"
	"github.com/example/mutation-judge/internal/model"
)

func TestR07S4MaxMutantsRetainsAccurateDiscoveryBoundsAndSpoolOrder(t *testing.T) {
	dir, verdicts := fourMutantFixture(t)
	cfg := config.Default()
	cfg.Cache = false
	cfg.Operators = []string{"boundary"}
	cfg.MaxMutants = 2
	req := Request{CWD: dir, Patterns: []string{"."}, Config: cfg}
	full, err := (Engine{Version: "test", Backend: &contentAwareFakeBackend{relPath: "p.go", verdicts: verdicts}}).Analyze(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if got := full.Bounds["discovered_before_bound"]; got != 4 {
		t.Fatalf("discovered_before_bound: %v", got)
	}
	if len(full.Results) != 2 {
		t.Fatalf("retained results: %d", len(full.Results))
	}
	if full.Summary.Generated != 2 {
		t.Fatalf("generated summary: %d", full.Summary.Generated)
	}
	metadata, spool, err := (Engine{Version: "test", Backend: &contentAwareFakeBackend{relPath: "p.go", verdicts: verdicts}}).AnalyzeSpool(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	defer spool.Close()
	if metadata.Bounds["discovered_before_bound"] != 4 || spool.Len() != 2 {
		t.Fatalf("streamed counts: %+v / %d", metadata.Bounds, spool.Len())
	}
	i := 0
	if err := spool.ForEach(func(r model.Result) error {
		if r.Mutation.ID != full.Results[i].Mutation.ID || r.Mutation.Diff != full.Results[i].Mutation.Diff || r.Verdict != full.Results[i].Verdict {
			t.Errorf("result %d differs", i)
		}
		i++
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}
