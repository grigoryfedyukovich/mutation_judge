package analysis

import (
	"context"
	"strings"
	"testing"

	"github.com/example/mutation-judge/internal/config"
	"github.com/example/mutation-judge/internal/model"
	"github.com/example/mutation-judge/internal/runner"
)

// R07-C2: A coverage-enabled test run is NOT evidence that the normal
// (mutant-equivalent) go test command passes. In particular, a test is
// allowed to inspect testing.CoverMode() and behave differently.
func TestR07C2RejectsCoverageOnlyPassingBaseline(t *testing.T) {
	dir := writeTestModule(t,
		"package p\nfunc F(x int) bool { return x > 0 }\n",
		`package p
import "testing"
func TestNeedsCoverage(t *testing.T) {
    if testing.CoverMode() == "" { t.Fatal("ordinary test fails without coverage") }
    if !F(1) { t.Fatal("unexpected") }
}
`)
	cfg := config.Default()
	cfg.Cache = false
	cfg.Operators = []string{"boundary"}
	report, err := (Engine{Version: "test", Backend: runner.GoTest{}}).Analyze(context.Background(), Request{
		CWD: dir, Patterns: []string{"."}, Config: cfg,
	})
	if err == nil || !strings.Contains(err.Error(), "uninstrumented baseline") ||
		!strings.Contains(err.Error(), "ordinary test fails without coverage") {
		t.Fatalf("coverage-only passing baseline must be rejected, report=%+v err=%v", report.Summary, err)
	}
	if len(report.Results) != 0 {
		t.Fatalf("no mutants may run when the ordinary baseline fails: %+v", report.Results)
	}
}

// Profiling is optional evidence. Tests may also require coverage to be
// disabled; a normal successful baseline must remain authoritative, and a
// failed instrumented run must not block mutation analysis or claim coverage.
func TestR07C2FailedOptionalProfilingDoesNotPoisonVerdicts(t *testing.T) {
	dir := writeTestModule(t,
		"package p\nfunc F(x int) bool { return x > 0 }\n",
		`package p
import "testing"
func TestMustNotInstrument(t *testing.T) {
    if testing.CoverMode() != "" { t.Fatal("coverage changes this test") }
    if !F(1) { t.Fatal("unexpected") }
}
`)
	cfg := config.Default()
	cfg.Cache = false
	cfg.Operators = []string{"boundary"}
	cfg.CoverageTestSelection = true
	report, err := (Engine{Version: "test", Backend: runner.GoTest{}}).Analyze(context.Background(), Request{
		CWD: dir, Patterns: []string{"."}, Config: cfg,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !report.Complete || len(report.Results) != 1 || report.Results[0].Verdict != model.VerdictSurvived {
		t.Fatalf("ordinary passing test should survive unchanged boundary: %+v", report.Summary)
	}
	if report.Results[0].CoverageKnown || report.Results[0].Covered {
		t.Fatalf("failed profiling must not claim coverage knowledge: %+v", report.Results[0])
	}
	if !strings.Contains(strings.Join(report.Warnings, "\n"), "baseline coverage unavailable") ||
		!strings.Contains(strings.Join(report.Warnings, "\n"), "coverage-test-selection disabled") {
		t.Fatalf("failed optional profiling must disable coverage selection with warning: %v", report.Warnings)
	}
}
