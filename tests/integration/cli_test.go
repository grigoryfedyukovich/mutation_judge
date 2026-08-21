package integration

import (
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"testing"
)

func TestBoundaryExampleEndToEnd(t *testing.T) {
	root := projectRoot()
	cmd := exec.Command("go", "run", "./cmd/mutation-judge", "--no-cache", "--progress=false", "--operators", "boundary", "--max-mutants", "1", "./examples/boundary")
	cmd.Dir = root
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("command failed: %v\n%s", err, out)
	}
	text := string(out)
	if !strings.Contains(text, "SURVIVED") || !strings.Contains(text, "suggested test:") {
		t.Fatalf("unexpected output:\n%s", text)
	}
}

func TestJSONOutputAndNestedParentDirectory(t *testing.T) {
	root := projectRoot()
	binary := buildBinary(t, root)
	outPath := filepath.Join(t.TempDir(), "nested", "report.json")
	cmd := exec.Command(binary, "--no-cache", "--progress=false", "--operators", "boundary", "--max-mutants", "1", "--format", "json", "--output", outPath, "./examples/boundary")
	cmd.Dir = root
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("command failed: %v\n%s", err, out)
	}
	f, err := os.Open(outPath)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	var report struct {
		ToolVersion string `json:"tool_version"`
		Timing      struct {
			RenderingMS int64 `json:"rendering_ms"`
		} `json:"timing"`
		Bounds map[string]any `json:"bounds"`
	}
	if err := json.NewDecoder(f).Decode(&report); err != nil {
		t.Fatal(err)
	}
	if report.ToolVersion != "0.1.3" || report.Timing.RenderingMS < 0 {
		t.Fatalf("unexpected report: %#v", report)
	}
}

func TestCIPolicyUsesConfiguredExitCode(t *testing.T) {
	root := projectRoot()
	binary := buildBinary(t, root)
	cmd := exec.Command(binary, "--no-cache", "--progress=false", "--operators", "boundary", "--ci-min-score", "100", "--ci-exit-code", "17", "./examples/boundary")
	cmd.Dir = root
	out, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("expected policy failure\n%s", out)
	}
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) || exitErr.ExitCode() != 17 {
		t.Fatalf("got %v, want exit 17\n%s", err, out)
	}
}

// TestCgoPackageEndToEnd proves the whole pipeline -- AST-based mutation
// discovery, the temporary sandbox module copy, and go test execution --
// works correctly against a package that uses cgo: frontend discovery
// must still find the plain-Go boundary comparison inside a file that
// also has `import "C"`, the sandbox copy must preserve whatever cgo
// needs to compile, and the backend must classify the result correctly.
func TestCgoPackageEndToEnd(t *testing.T) {
	if os.Getenv("CGO_ENABLED") == "0" {
		t.Skip("cgo is disabled in this environment")
	}
	root := projectRoot()
	binary := buildBinary(t, root)
	cmd := exec.Command(binary, "--no-cache", "--progress=false", "--operators", "boundary", "./tests/integration/testdata/cgo")
	cmd.Dir = root
	out, err := cmd.CombinedOutput()
	text := string(out)
	if err != nil {
		t.Fatalf("command failed: %v\n%s", err, text)
	}
	if !strings.Contains(text, "KILLED") {
		t.Fatalf("expected the boundary mutant in the cgo fixture to be killed:\n%s", text)
	}
	if !strings.Contains(text, "1 killed, 0 survived") {
		t.Fatalf("unexpected summary for the cgo fixture:\n%s", text)
	}
}

// TestPackageInitPanicEndToEnd proves a package that fails before any
// test can run for a runtime reason (here, a panic in init()) is
// classified KILLED rather than INVALID: the package compiled fine, so
// this is a real mutant kill, not an uncompilable mutant.
func TestPackageInitPanicEndToEnd(t *testing.T) {
	root := projectRoot()
	binary := buildBinary(t, root)
	cmd := exec.Command(binary, "--no-cache", "--progress=false", "--operators", "boundary", "./tests/integration/testdata/initpanic")
	cmd.Dir = root
	out, err := cmd.CombinedOutput()
	text := string(out)
	if err != nil {
		t.Fatalf("command failed: %v\n%s", err, text)
	}
	if !strings.Contains(text, "KILLED") {
		t.Fatalf("expected the init()-panicking mutant to be KILLED, not INVALID:\n%s", text)
	}
	if strings.Contains(text, "INVALID") {
		t.Fatalf("init() panic must not be classified as a build failure:\n%s", text)
	}
}

// TestCustomTestMainEndToEnd proves a package whose TestMain calls
// os.Exit with a nonzero status (bypassing the normal per-test reporting
// entirely) is still classified KILLED, with no test incorrectly
// attributed, rather than falling through to UNKNOWN or INVALID.
func TestCustomTestMainEndToEnd(t *testing.T) {
	root := projectRoot()
	binary := buildBinary(t, root)
	cmd := exec.Command(binary, "--no-cache", "--progress=false", "--operators", "boundary", "./tests/integration/testdata/custommain")
	cmd.Dir = root
	out, err := cmd.CombinedOutput()
	text := string(out)
	if err != nil {
		t.Fatalf("command failed: %v\n%s", err, text)
	}
	if !strings.Contains(text, "KILLED") {
		t.Fatalf("expected the mutant to be KILLED via a custom TestMain exit:\n%s", text)
	}
	if strings.Contains(text, "killed by:") {
		t.Fatalf("a TestMain-driven exit never runs a specific test, so none should be attributed:\n%s", text)
	}
}

// TestBuildTagExcludedFileIsNeverMutatedEndToEnd proves discovery only
// mutates files go list actually places in the current build (GoFiles),
// not every .go file found by walking the package directory: a file
// tagged for a platform that never matches must contribute zero
// mutants, even though it contains an otherwise-mutable comparison.
func TestBuildTagExcludedFileIsNeverMutatedEndToEnd(t *testing.T) {
	root := projectRoot()
	binary := buildBinary(t, root)
	cmd := exec.Command(binary, "--no-cache", "--progress=false", "--operators", "boundary", "--format", "json", "./tests/integration/testdata/buildtags")
	cmd.Dir = root
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("command failed: %v\n%s", err, out)
	}
	var report struct {
		Results []struct {
			Mutation struct {
				Span struct {
					File string `json:"file"`
				} `json:"span"`
			} `json:"mutation"`
		} `json:"results"`
	}
	if err := json.Unmarshal(out, &report); err != nil {
		t.Fatalf("decode report: %v\n%s", err, out)
	}
	if len(report.Results) != 1 {
		t.Fatalf("want exactly 1 mutant (from included.go only), got %d:\n%s", len(report.Results), out)
	}
	if !strings.HasSuffix(report.Results[0].Mutation.Span.File, "included.go") {
		t.Fatalf("mutant came from %q, want included.go; the build-tag-excluded file must never be mutated", report.Results[0].Mutation.Span.File)
	}
}

// TestNarrowTestScopeKillsAcrossExternalTestOnlyDependency is the
// critical correctness test for --narrow-test-scope: a mutation-testing
// tool that silently reports a false SURVIVED because it narrowed test
// scope incorrectly would be actively harmful, worse than not narrowing
// at all. tests/integration/testdata/scoping is built specifically so a
// mutant in package a is *not* caught by a's own test, and *is* caught
// only by package d's external ("package d_test") test file -- a
// dependency reachable only through a _test.go file in a different
// package, which a naive walk of ordinary build Imports would miss
// entirely. With scoping enabled, this must still be KILLED.
func TestNarrowTestScopeKillsAcrossExternalTestOnlyDependency(t *testing.T) {
	root := projectRoot()
	binary := buildBinary(t, root)
	cmd := exec.Command(binary, "--no-cache", "--progress=false", "--operators", "boundary",
		"--narrow-test-scope", "./tests/integration/testdata/scoping/...")
	cmd.Dir = root
	out, err := cmd.CombinedOutput()
	text := string(out)
	if err != nil {
		t.Fatalf("command failed: %v\n%s", err, text)
	}
	if !strings.Contains(text, "KILLED") {
		t.Fatalf("expected the mutant to be KILLED via d's external test even with scoping on:\n%s", text)
	}
	if strings.Contains(text, "SURVIVED") {
		t.Fatalf("a SURVIVED result here would mean scoping silently missed a real cross-package dependency:\n%s", text)
	}
	if !strings.Contains(text, "killed by: TestClassifyBoundary") {
		t.Fatalf("expected attribution to d's TestClassifyBoundary specifically:\n%s", text)
	}

	// The unambiguous proof that scoping actually narrowed something
	// (not just "safely did nothing"): package unrelated's test sleeps
	// 3s and has no relationship to package a. Baseline always validates
	// the *full* pattern set regardless of scoping (it's checking the
	// person's whole test suite passes before any mutation happens, not
	// something scoping should ever skip) so overall wall time alone
	// isn't the right signal -- it will always include that 3s+ from
	// baseline. The mutant-execution phase specifically must not.
	mutantsMS := parseMutantsTimingMS(t, text)
	if mutantsMS >= 3000 {
		t.Fatalf("mutants phase took %dms; --narrow-test-scope should have excluded the unrelated package's slow, irrelevant test from mutant execution specifically (full output:\n%s)", mutantsMS, text)
	}
}

// TestWithoutNarrowTestScopeRunsTheSlowUnrelatedPackage is the control
// for the timing assertion above: it proves the fixture's 3-second
// signal is real -- caused by scoping, not some other reason the
// unrelated package's test wouldn't run anyway -- by confirming the
// *default* (scoping off) behavior does pay that cost.
func TestWithoutNarrowTestScopeRunsTheSlowUnrelatedPackage(t *testing.T) {
	root := projectRoot()
	binary := buildBinary(t, root)
	cmd := exec.Command(binary, "--no-cache", "--progress=false", "--operators", "boundary",
		"./tests/integration/testdata/scoping/...")
	cmd.Dir = root
	out, err := cmd.CombinedOutput()
	text := string(out)
	if err != nil {
		t.Fatalf("command failed: %v\n%s", err, text)
	}
	// Without scoping, every mutant re-runs the full pattern set, which
	// includes the unrelated package's 3s test -- this is the direct
	// counterpart of the mutants-phase assertion in the scoped test
	// above, isolating the same phase for a fair comparison.
	mutantsMS := parseMutantsTimingMS(t, text)
	if mutantsMS < 3000 {
		t.Fatalf("mutants phase took only %dms without --narrow-test-scope; expected the unrelated package's 3s test to run as part of the default full pattern set (full output:\n%s)", mutantsMS, text)
	}
}

// parseMutantsTimingMS extracts the "mutants=Nms" component from a text
// report's trailing timing line, to assert on the mutant-execution
// phase specifically rather than total wall time, which always includes
// baseline (see the two tests above for why that distinction matters
// here).
func parseMutantsTimingMS(t *testing.T, text string) int {
	t.Helper()
	re := regexp.MustCompile(`mutants=(\d+)ms`)
	m := re.FindStringSubmatch(text)
	if m == nil {
		t.Fatalf("could not find a mutants=Nms timing component in output:\n%s", text)
	}
	ms, err := strconv.Atoi(m[1])
	if err != nil {
		t.Fatalf("mutants timing %q did not parse as an integer: %v", m[1], err)
	}
	return ms
}

// TestWorkersProducesSameResultsAsSequential is the real-toolchain
// counterpart to the fake-backend-based parallel tests in
// internal/analysis: the same fixture (four independent boundary
// mutants, some killed, some surviving) run once with the default
// sequential execution and once with --workers 3 must produce identical
// per-mutant verdicts in identical order, this time through the actual
// compiled binary and real go test invocations rather than a
// content-aware fake backend.
func TestWorkersProducesSameResultsAsSequential(t *testing.T) {
	root := projectRoot()
	binary := buildBinary(t, root)

	runJSON := func(extraArgs ...string) reportSummary {
		args := append([]string{"--no-cache", "--progress=false", "--operators", "boundary", "--format", "json"}, extraArgs...)
		args = append(args, "./tests/integration/testdata/workers")
		cmd := exec.Command(binary, args...)
		cmd.Dir = root
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("command failed: %v\n%s", err, out)
		}
		var report reportSummary
		if err := json.Unmarshal(out, &report); err != nil {
			t.Fatalf("decode report: %v\n%s", err, out)
		}
		return report
	}

	sequential := runJSON()
	parallel := runJSON("--workers", "3")

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

type reportSummary struct {
	Complete bool `json:"complete"`
	Summary  struct {
		Generated int     `json:"generated"`
		Killed    int     `json:"killed"`
		Survived  int     `json:"survived"`
		Invalid   int     `json:"invalid"`
		Score     float64 `json:"score"`
	} `json:"summary"`
	Results []struct {
		Mutation struct {
			ID string `json:"id"`
		} `json:"mutation"`
		Verdict string `json:"verdict"`
	} `json:"results"`
}

func projectRoot() string {
	_, here, _, _ := runtime.Caller(0)
	return filepath.Clean(filepath.Join(filepath.Dir(here), "..", ".."))
}

func buildBinary(t *testing.T, root string) string {
	t.Helper()
	binary := filepath.Join(t.TempDir(), "mutation-judge")
	cmd := exec.Command("go", "build", "-o", binary, "./cmd/mutation-judge")
	cmd.Dir = root
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build failed: %v\n%s", err, out)
	}
	return binary
}
