package analysis

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/example/mutation-judge/internal/config"
	"github.com/example/mutation-judge/internal/model"
	"github.com/example/mutation-judge/internal/runner"
)

type sequenceBackend struct{ calls int }

func (s *sequenceBackend) Run(_ context.Context, _ runner.Request) runner.Result {
	s.calls++
	if s.calls == 1 {
		return runner.Result{Verdict: model.VerdictSurvived}
	}
	return runner.Result{Verdict: model.VerdictKilled, Tests: []string{"TestBoundary"}}
}

func (s *sequenceBackend) Name() string    { return "sequence" }
func (s *sequenceBackend) Version() string { return "v1" }

func TestEngineWithFakeBackend(t *testing.T) {
	d := testProject(t, "package p\nfunc Positive(n int) bool { return n > 0 }\n")
	cfg := config.Default()
	cfg.Cache = false
	b := &sequenceBackend{}
	progress := 0
	r, err := (Engine{Version: "test", Backend: b, Progress: func(Progress) { progress++ }}).Analyze(context.Background(), Request{CWD: d, Patterns: []string{"."}, Config: cfg})
	if err != nil {
		t.Fatal(err)
	}
	if r.ToolVersion != "test" || r.Summary.Generated != 1 || r.Summary.Killed != 1 || progress != 1 {
		t.Fatalf("unexpected report: %#v progress=%d", r, progress)
	}
	if r.Bounds["backend_name"] != "sequence" || r.Bounds["backend_version"] != "v1" {
		t.Fatalf("missing backend identity: %#v", r.Bounds)
	}
}

func TestMaxMutantsReportsDiscoveredAndExecutedBounds(t *testing.T) {
	d := testProject(t, "package p\nfunc InRange(n int) bool { return n > 0 && n < 10 }\n")
	cfg := config.Default()
	cfg.Cache = false
	cfg.Operators = []string{"boundary"}
	cfg.MaxMutants = 1
	b := &sequenceBackend{}
	r, err := (Engine{Version: "test", Backend: b}).Analyze(context.Background(), Request{CWD: d, Patterns: []string{"."}, Config: cfg})
	if err != nil {
		t.Fatal(err)
	}
	if r.Bounds["discovered_before_bound"] != 2 || r.Bounds["retained_after_bound"] != 1 || r.Bounds["completed_mutants"] != 1 || r.Summary.Generated != 1 {
		t.Fatalf("incorrect bounds: %#v summary=%#v", r.Bounds, r.Summary)
	}
}

type cancelBackend struct {
	calls  int
	cancel context.CancelFunc
}

func (b *cancelBackend) Run(_ context.Context, _ runner.Request) runner.Result {
	b.calls++
	if b.calls == 1 {
		return runner.Result{Verdict: model.VerdictSurvived}
	}
	b.cancel()
	return runner.Result{Verdict: model.VerdictKilled, Tests: []string{"TestFirst"}}
}

func TestCancellationProducesLabeledPartialReport(t *testing.T) {
	d := testProject(t, "package p\nfunc InRange(n int) bool { return n > 0 && n < 10 }\n")
	cfg := config.Default()
	cfg.Cache = false
	cfg.Operators = []string{"boundary"}
	ctx, cancel := context.WithCancel(context.Background())
	b := &cancelBackend{cancel: cancel}
	r, err := (Engine{Version: "test", Backend: b}).Analyze(ctx, Request{CWD: d, Patterns: []string{"."}, Config: cfg})
	if err != nil {
		t.Fatal(err)
	}
	if r.Complete || len(r.Results) != 1 {
		t.Fatalf("expected one-result incomplete report: complete=%v results=%d", r.Complete, len(r.Results))
	}
}

func TestTestCommandIncludesTimeout(t *testing.T) {
	cfg := config.Default()
	cfg.Timeout = 3 * time.Second
	cfg.TestRun = "TestParser"
	got := testCommand(cfg, []string{"./pkg"})
	want := []string{"go", "test", "-count=1", "-timeout", "3s", "-run", "TestParser", "./pkg"}
	if len(got) != len(want) {
		t.Fatalf("got %v want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %v want %v", got, want)
		}
	}
}

func testProject(t *testing.T, source string) string {
	t.Helper()
	d := t.TempDir()
	if err := os.WriteFile(filepath.Join(d, "go.mod"), []byte("module example.test/p\n\ngo 1.22\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(d, "p.go"), []byte(source), 0o644); err != nil {
		t.Fatal(err)
	}
	return d
}
