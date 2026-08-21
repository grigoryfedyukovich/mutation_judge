package analysis

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/example/mutation-judge/internal/config"
	covermap "github.com/example/mutation-judge/internal/coverage"
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

// contentAwareFakeBackend is a race-safe fake backend for testing the
// parallel execution path itself (ordering, cancellation, hard-error
// propagation). Its verdict is a pure function of which mutant actually
// ran, read back from the mutated file in the sandbox req.Root points
// at -- not of call count or arrival order, which is what makes it
// possible to prove concurrent workers never cross-contaminate results:
// a backend whose decisions depended on order would only prove that
// toy order-dependent logic behaves consistently under scheduling, not
// that this package correctly keeps each mutant's own result attached
// to it regardless of which goroutine or sandbox handled it. A small
// varying delay (from an atomic counter, not from shared decision
// state) encourages genuine goroutine interleaving rather than
// accidentally-sequential execution that would make these tests weaker
// than they look.
type contentAwareFakeBackend struct {
	relPath  string // file to read back, relative to req.Root
	verdicts map[string]runner.Result
	calls    atomic.Int64
}

func (b *contentAwareFakeBackend) Run(_ context.Context, req runner.Request) runner.Result {
	n := b.calls.Add(1)
	time.Sleep(time.Duration(n%3) * 5 * time.Millisecond)
	data, err := os.ReadFile(filepath.Join(req.Root, b.relPath))
	if err != nil {
		return runner.Result{Verdict: model.VerdictUnknown}
	}
	content := string(data)
	for marker, result := range b.verdicts {
		if strings.Contains(content, marker) {
			return result
		}
	}
	// No mutation marker present: this is the baseline (unmutated) run,
	// which must survive for the analysis to proceed to any mutants at
	// all.
	return runner.Result{Verdict: model.VerdictSurvived}
}

func (b *contentAwareFakeBackend) Name() string    { return "content-aware-fake" }
func (b *contentAwareFakeBackend) Version() string { return "v1" }

// fourMutantFixture returns a project with four independent boundary
// comparisons, one per function, chosen so each one's mutation is
// distinguishable by a unique literal in the mutated source
// (">= 0", ">= 1", ">= 2", ">= 3") and so the fixture is large enough
// relative to typical small worker counts (4 mutants, 3 workers below)
// to exercise real queuing, not just one mutant per worker.
func fourMutantFixture(t *testing.T) (dir string, verdicts map[string]runner.Result) {
	t.Helper()
	dir = testProject(t, "package p\n"+
		"func A(n int) bool { return n > 0 }\n"+
		"func B(n int) bool { return n > 1 }\n"+
		"func C(n int) bool { return n > 2 }\n"+
		"func D(n int) bool { return n > 3 }\n")
	verdicts = map[string]runner.Result{
		">= 0": {Verdict: model.VerdictSurvived},
		">= 1": {Verdict: model.VerdictKilled, Tests: []string{"TestB"}},
		">= 2": {Verdict: model.VerdictSurvived},
		">= 3": {Verdict: model.VerdictKilled, Tests: []string{"TestD"}},
	}
	return dir, verdicts
}

// TestParallelExecutionMatchesSequentialResults is the core correctness
// test for --workers: the exact same set of mutants, run once
// sequentially and once with 3 concurrent workers, must produce
// identical per-mutant verdicts in identical (discovery) order. Any
// divergence here would mean either a mutant's result got attached to
// the wrong sandbox/worker, or output ordering isn't actually
// deterministic despite parallel, out-of-order completion.
func TestParallelExecutionMatchesSequentialResults(t *testing.T) {
	dir, verdicts := fourMutantFixture(t)
	cfg := config.Default()
	cfg.Cache = false
	cfg.Operators = []string{"boundary"}

	sequential, err := (Engine{Version: "test", Backend: &contentAwareFakeBackend{relPath: "p.go", verdicts: verdicts}}).
		Analyze(context.Background(), Request{CWD: dir, Patterns: []string{"."}, Config: cfg})
	if err != nil {
		t.Fatal(err)
	}

	cfg.Workers = 3
	parallel, err := (Engine{Version: "test", Backend: &contentAwareFakeBackend{relPath: "p.go", verdicts: verdicts}}).
		Analyze(context.Background(), Request{CWD: dir, Patterns: []string{"."}, Config: cfg})
	if err != nil {
		t.Fatal(err)
	}

	if !parallel.Complete {
		t.Fatal("parallel run reported incomplete")
	}
	if len(sequential.Results) != len(parallel.Results) {
		t.Fatalf("result count differs: sequential=%d parallel=%d", len(sequential.Results), len(parallel.Results))
	}
	for i := range sequential.Results {
		s, p := sequential.Results[i], parallel.Results[i]
		if s.Mutation.ID != p.Mutation.ID {
			t.Fatalf("result %d: order differs: sequential=%s parallel=%s", i, s.Mutation.ID, p.Mutation.ID)
		}
		if s.Verdict != p.Verdict {
			t.Fatalf("result %d (%s): verdict differs: sequential=%s parallel=%s", i, s.Mutation.ID, s.Verdict, p.Verdict)
		}
	}
	if sequential.Summary != parallel.Summary {
		t.Fatalf("summary differs: sequential=%#v parallel=%#v", sequential.Summary, parallel.Summary)
	}
}

// TestParallelExecutionIsDeterministicAcrossRuns runs the same analysis
// with workers=3 three separate times and confirms identical result
// ordering every time, despite whatever real goroutine-scheduling
// nondeterminism actually occurred underneath (the varying delay in
// contentAwareFakeBackend is specifically there to make different
// completion orders across the three runs likely, not just possible).
func TestParallelExecutionIsDeterministicAcrossRuns(t *testing.T) {
	dir, verdicts := fourMutantFixture(t)
	cfg := config.Default()
	cfg.Cache = false
	cfg.Operators = []string{"boundary"}
	cfg.Workers = 3

	var runs [][]string
	for i := 0; i < 3; i++ {
		r, err := (Engine{Version: "test", Backend: &contentAwareFakeBackend{relPath: "p.go", verdicts: verdicts}}).
			Analyze(context.Background(), Request{CWD: dir, Patterns: []string{"."}, Config: cfg})
		if err != nil {
			t.Fatal(err)
		}
		var order []string
		for _, res := range r.Results {
			order = append(order, res.Mutation.ID)
		}
		runs = append(runs, order)
	}
	for i := 1; i < len(runs); i++ {
		if len(runs[i]) != len(runs[0]) {
			t.Fatalf("run %d has %d results, run 0 had %d", i, len(runs[i]), len(runs[0]))
		}
		for j := range runs[0] {
			if runs[i][j] != runs[0][j] {
				t.Fatalf("run %d order diverges from run 0 at position %d: %v vs %v", i, j, runs[i], runs[0])
			}
		}
	}
}

// TestParallelExecutionCancellationProducesPartialReport is the
// parallel counterpart to TestCancellationProducesLabeledPartialReport:
// cancelling mid-run must still produce a usable partial report
// (Complete=false, containing whatever did finish) rather than hanging
// or losing results.
func TestParallelExecutionCancellationProducesPartialReport(t *testing.T) {
	dir, verdicts := fourMutantFixture(t)
	cfg := config.Default()
	cfg.Cache = false
	cfg.Operators = []string{"boundary"}
	cfg.Workers = 2

	ctx, cancel := context.WithCancel(context.Background())
	backend := &cancelAfterNFakeBackend{n: 1, cancel: cancel, relPath: "p.go", verdicts: verdicts}
	r, err := (Engine{Version: "test", Backend: backend}).Analyze(ctx, Request{CWD: dir, Patterns: []string{"."}, Config: cfg})
	if err != nil {
		t.Fatal(err)
	}
	if r.Complete {
		t.Fatal("expected an incomplete report after mid-run cancellation")
	}
	if len(r.Results) == 0 || len(r.Results) >= 4 {
		t.Fatalf("expected a partial (nonzero, not all 4) result count, got %d", len(r.Results))
	}
}

// cancelAfterNFakeBackend cancels the shared context after the Nth call
// completes (guarded by a mutex, since multiple workers call this
// concurrently), simulating an external SIGINT/SIGTERM arriving mid-run.
type cancelAfterNFakeBackend struct {
	mu       sync.Mutex
	calls    int
	n        int
	cancel   context.CancelFunc
	relPath  string
	verdicts map[string]runner.Result
}

func (b *cancelAfterNFakeBackend) Run(_ context.Context, req runner.Request) runner.Result {
	time.Sleep(5 * time.Millisecond)
	data, _ := os.ReadFile(filepath.Join(req.Root, b.relPath))
	content := string(data)
	result := runner.Result{Verdict: model.VerdictSurvived} // no marker present = baseline (unmutated) run
	isMutant := false
	for marker, r := range b.verdicts {
		if strings.Contains(content, marker) {
			result = r
			isMutant = true
			break
		}
	}
	if isMutant {
		b.mu.Lock()
		b.calls++
		if b.calls == b.n {
			b.cancel()
		}
		b.mu.Unlock()
	}
	return result
}

func (b *cancelAfterNFakeBackend) Name() string    { return "cancel-after-n-fake" }
func (b *cancelAfterNFakeBackend) Version() string { return "v1" }

// TestParallelExecutionHardErrorStopsAllWorkers proves a hard error in
// one worker (an Apply/restore failure, not an ordinary verdict) stops
// the whole parallel run promptly and propagates the error, rather than
// hanging waiting for other workers or silently swallowing it. This is
// simulated by pointing WorkRel at a path that makes workspace.Apply
// fail for every mutant it's asked to apply.
func TestParallelExecutionHardErrorStopsAllWorkers(t *testing.T) {
	dir, _ := fourMutantFixture(t)
	cfg := config.Default()
	cfg.Cache = false
	cfg.Operators = []string{"boundary"}
	cfg.Workers = 3

	backend := &neverCalledBackend{t: t}
	engine := Engine{Version: "test", Backend: backend}
	prepared, err := engine.prepare(Request{CWD: dir, Patterns: []string{"."}, Config: cfg})
	if err != nil {
		t.Fatal(err)
	}
	defer prepared.cleanup()
	// Corrupt every mutant's file path so workspace.Apply fails for all
	// of them -- a hard error, not a verdict -- exercising the
	// propagation path directly rather than depending on a harder-to-
	// arrange real filesystem failure.
	for i := range prepared.mutants {
		prepared.mutants[i].Span.File = "does-not-exist.go"
	}
	_, _, _, _, err = engine.executeMutantsParallel(context.Background(), Request{CWD: dir, Patterns: []string{"."}, Config: cfg}, prepared, covermap.Map{}, false)
	if err == nil {
		t.Fatal("expected a hard error to propagate")
	}
}

type neverCalledBackend struct{ t *testing.T }

func (b *neverCalledBackend) Run(context.Context, runner.Request) runner.Result {
	b.t.Fatal("backend should never be called: workspace.Apply must fail before this for every mutant")
	return runner.Result{}
}
func (b *neverCalledBackend) Name() string    { return "never-called" }
func (b *neverCalledBackend) Version() string { return "v1" }

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

// A cache write failure must not fail the whole analysis (the mutant was
// still correctly executed and classified) but must not be silently
// swallowed either: it needs to show up as an evidence field on the
// report. CacheDir is pointed at a path blocked by an existing regular
// file, which makes os.MkdirAll fail regardless of the user running the
// test (a permission-bits-based approach would not reproduce reliably
// when tests run as root).
// TestCacheKeyIsInsensitiveToWorkerCount is a permanent regression test
// for a real bug found by testing the finished --workers feature rather
// than assuming it worked: worker count has no bearing on any
// individual mutant's own test outcome, so a cache entry from one
// worker count must be reused when the same analysis is later run with
// a different one. Confirmed via the exact scenario that caught it: run
// once with Workers=3, then again with Workers=1, and check the second
// run reports every mutant as a cache hit.
func TestCacheKeyIsInsensitiveToWorkerCount(t *testing.T) {
	dir, verdicts := fourMutantFixture(t)
	cfg := config.Default()
	cfg.Operators = []string{"boundary"}
	cfg.CacheDir = filepath.Join(dir, ".mutation-judge", "cache")

	cfg.Workers = 3
	first, err := (Engine{Version: "test", Backend: &contentAwareFakeBackend{relPath: "p.go", verdicts: verdicts}}).
		Analyze(context.Background(), Request{CWD: dir, Patterns: []string{"."}, Config: cfg})
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range first.Results {
		if r.Cached {
			t.Fatalf("expected a cold cache on the first run, but %s was already reported cached", r.Mutation.ID)
		}
	}

	cfg.Workers = 1
	second, err := (Engine{Version: "test", Backend: &contentAwareFakeBackend{relPath: "p.go", verdicts: verdicts}}).
		Analyze(context.Background(), Request{CWD: dir, Patterns: []string{"."}, Config: cfg})
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range second.Results {
		if !r.Cached {
			t.Fatalf("expected %s to be a cache hit after only changing --workers, got a fresh execution", r.Mutation.ID)
		}
	}
}

func TestCacheWriteFailureIsReportedAsWarningNotFatal(t *testing.T) {
	d := testProject(t, "package p\nfunc Positive(n int) bool { return n > 0 }\n")
	blocker := filepath.Join(d, "cache-blocked-by-a-file")
	if err := os.WriteFile(blocker, []byte("not a directory"), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg := config.Default()
	cfg.Cache = true
	cfg.CacheDir = blocker
	b := &sequenceBackend{}
	r, err := (Engine{Version: "test", Backend: b}).Analyze(context.Background(), Request{CWD: d, Patterns: []string{"."}, Config: cfg})
	if err != nil {
		t.Fatalf("cache write failure must be non-fatal, got error: %v", err)
	}
	if r.Summary.Generated != 1 || r.Summary.Killed != 1 {
		t.Fatalf("the mutant result itself must still be correct despite the cache failure: %#v", r.Summary)
	}
	if len(r.Warnings) != 1 {
		t.Fatalf("expected exactly one warning, got %v", r.Warnings)
	}
	if !strings.Contains(r.Warnings[0], "cache write failed") {
		t.Fatalf("warning does not describe a cache write failure: %q", r.Warnings[0])
	}
}

// mutantTestScope's fallback behavior is safety-critical: any
// uncertainty must widen back to the full pattern set rather than risk
// running too little, so each source of uncertainty is tested directly
// rather than relying only on the end-to-end fixture in
// tests/integration (TestNarrowTestScopeKillsAcrossExternalTestOnlyDependency),
// which exercises the happy path.
func TestMutantTestScopeFallsBackWhenUncertain(t *testing.T) {
	req := Request{Patterns: []string{"./..."}, Config: config.Config{NarrowTestScope: true}}
	mut := model.Mutation{Span: model.Span{File: "pkg/foo.go"}}

	t.Run("disabled entirely", func(t *testing.T) {
		off := Request{Patterns: []string{"./..."}, Config: config.Config{NarrowTestScope: false}}
		got := mutantTestScope(off, preparedAnalysis{
			filePackage: map[string]string{"pkg/foo.go": "example/pkg"},
			testScopes:  map[string][]string{"example/pkg": {"example/other"}},
		}, mut)
		assertScopeEqual(t, got, off.Patterns)
	})

	t.Run("file not found in the package map", func(t *testing.T) {
		got := mutantTestScope(req, preparedAnalysis{
			filePackage: map[string]string{}, // mut's file isn't in here
			testScopes:  map[string][]string{"example/pkg": {"example/other"}},
		}, mut)
		assertScopeEqual(t, got, req.Patterns)
	})

	t.Run("package found but has no recorded scope", func(t *testing.T) {
		got := mutantTestScope(req, preparedAnalysis{
			filePackage: map[string]string{"pkg/foo.go": "example/pkg"},
			testScopes:  map[string][]string{}, // no entry for example/pkg at all
		}, mut)
		assertScopeEqual(t, got, req.Patterns)
	})

	t.Run("scope resolves normally when both are present", func(t *testing.T) {
		got := mutantTestScope(req, preparedAnalysis{
			filePackage: map[string]string{"pkg/foo.go": "example/pkg"},
			testScopes:  map[string][]string{"example/pkg": {"example/pkg", "example/other"}},
		}, mut)
		assertScopeEqual(t, got, []string{"example/pkg", "example/other"})
	})
}

func assertScopeEqual(t *testing.T, got, want []string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("scope = %v, want %v", got, want)
	}
	for i := range got {
		if got[i] != want[i] {
			t.Fatalf("scope = %v, want %v", got, want)
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
