package analysis

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/example/mutation-judge/internal/config"
	"github.com/example/mutation-judge/internal/model"
	"github.com/example/mutation-judge/internal/runner"
)

// Exercise the actual go test coverage writer and the analysis report, not
// just a synthetic profile. A test that never calls F gives a zero-count
// coverage record for F; this is known-uncovered, not unknown instrumentation.
func TestF12RealGoZeroCountCoverageKnown(t *testing.T) {
	dir := writeTestModule(t,
		"package p\nfunc F(x int) bool { return x > 0 }\n",
		"package p\nimport \"testing\"\nfunc TestNothing(t *testing.T) {}\n")
	cfg := config.Default()
	cfg.Cache = false
	cfg.Operators = []string{"boundary"}
	report, err := (Engine{Version: "test", Backend: runner.GoTest{}}).Analyze(context.Background(), Request{
		CWD: dir, Patterns: []string{"."}, Config: cfg,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !report.Complete || len(report.Results) != 1 {
		t.Fatalf("unexpected result count/completeness: %d, %t", len(report.Results), report.Complete)
	}
	got := report.Results[0]
	if got.Verdict != model.VerdictSurvived || !got.CoverageKnown || got.Covered {
		t.Fatalf("unexecuted F must be known-uncovered with surviving mutant: %+v", got)
	}
}

type f12MalformedCoverageBackend struct{}

func (f12MalformedCoverageBackend) Name() string    { return "f12-malformed-profile" }
func (f12MalformedCoverageBackend) Version() string { return "v1" }
func (f12MalformedCoverageBackend) Run(_ context.Context, req runner.Request) runner.Result {
	if req.CoverageOut != "" {
		profile := "mode: count\nexample.test/pertest/p.go:1.1,2.3 1 0\n"
		if req.TestRun != "" {
			profile += "example.test/pertest/p.go:3.NOPE,4.5 1 1\n"
		}
		if err := os.MkdirAll(filepath.Dir(req.CoverageOut), 0o755); err != nil {
			return runner.Result{Verdict: model.VerdictUnknown, Output: err.Error()}
		}
		if err := os.WriteFile(req.CoverageOut, []byte(profile), 0o644); err != nil {
			return runner.Result{Verdict: model.VerdictUnknown, Output: err.Error()}
		}
	}
	// Kill the mutant only when the original full TestRun selection is
	// retained. A malformed per-test profile must never narrow execution.
	src, err := os.ReadFile(filepath.Join(req.Root, "p.go"))
	if err != nil {
		return runner.Result{Verdict: model.VerdictUnknown, Output: err.Error()}
	}
	if strings.Contains(string(src), ">= 0") && req.TestRun == "" {
		return runner.Result{Verdict: model.VerdictKilled}
	}
	return runner.Result{Verdict: model.VerdictSurvived}
}

func TestF12MalformedPerTestProfileDisablesSelection(t *testing.T) {
	dir := writeTestModule(t,
		"package p\nfunc F(x int) bool { return x > 0 }\n",
		"package p\nimport \"testing\"\nfunc TestWeak(t *testing.T) { F(1) }\n")
	cfg := config.Default()
	cfg.Cache = false
	cfg.Operators = []string{"boundary"}
	cfg.CoverageTestSelection = true
	report, err := (Engine{Version: "test", Backend: f12MalformedCoverageBackend{}}).Analyze(context.Background(), Request{
		CWD: dir, Patterns: []string{"."}, Config: cfg,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !report.Complete || len(report.Results) != 1 || report.Results[0].Verdict != model.VerdictKilled {
		t.Fatalf("must retain unselected broad run when profile malformed: %+v", report.Summary)
	}
	if len(report.Warnings) == 0 || !strings.Contains(strings.Join(report.Warnings, "\n"), "parsing coverage profile") {
		t.Fatalf("expected warning about malformed profiling evidence, got %v", report.Warnings)
	}
}
