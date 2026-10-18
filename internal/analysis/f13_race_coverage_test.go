package analysis

import (
	"context"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/example/mutation-judge/internal/config"
	"github.com/example/mutation-judge/internal/model"
	"github.com/example/mutation-judge/internal/runner"
)

// Exercise baseline coverage, independent per-test coverage and mutant execution
// through the real analyzer while -race is inherited via GOFLAGS.
func TestF13RaceCoverageSelectionEndToEnd(t *testing.T) {
	if out, err := exec.Command("go", "env", "CGO_ENABLED").Output(); err != nil || strings.TrimSpace(string(out)) != "1" {
		t.Skip("race detector requires a cgo-enabled Go toolchain")
	}
	t.Setenv("GOFLAGS", "-race")
	root := writeTestModule(t, "package p\nfunc F(x int) bool { return x > 0 }\n", `package p
import "testing"
func TestWeak(t *testing.T) { if !F(1) { t.Fatal("positive") } }
func TestBoundary(t *testing.T) { if F(0) { t.Fatal("zero") } }
`)
	cfg := config.Default()
	cfg.Cache = false
	cfg.Timeout = 120 * time.Second
	cfg.Operators = []string{"boundary"}
	cfg.CoverageTestSelection = true
	report, err := (Engine{Version: "test", Backend: runner.GoTest{}}).Analyze(context.Background(), Request{
		CWD: root, Patterns: []string{"."}, Config: cfg,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !report.Complete || len(report.Results) != 1 || report.Results[0].Verdict != model.VerdictKilled {
		t.Fatalf("race-enabled selection should kill boundary mutant: complete=%v summary=%+v warnings=%v", report.Complete, report.Summary, report.Warnings)
	}
	for _, warning := range report.Warnings {
		if strings.Contains(warning, "coverage-test-selection disabled") {
			t.Fatalf("race-enabled profiling unexpectedly fell back: %v", report.Warnings)
		}
	}
}
