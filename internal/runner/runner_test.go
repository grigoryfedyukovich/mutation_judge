package runner

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/example/mutation-judge/internal/model"
)

func TestFailingTests(t *testing.T) {
	got := failingTests("--- FAIL: TestB (0.00s)\n--- FAIL: TestA (0.00s)\n")
	if len(got) != 2 || got[0] != "TestA" || got[1] != "TestB" {
		t.Fatalf("%v", got)
	}
}

// classifyEvents is exercised directly (not only through GoTest.Run
// against a real go binary) specifically because a real-world go
// toolchain's exact -json output for a given failure kind can differ
// across platforms/versions in ways that are impractical to reproduce
// reliably in every CI environment -- which is exactly how the bug these
// two tests pin down slipped past the original, integration-only test
// suite: on Linux, "build constraints exclude all Go files" produced no
// JSON events at all (exercising the separate toolchain-failure fallback
// path in GoTest.Run, not classifyEvents), while on at least one other
// platform/Go version it instead produces a real package-level "output"
// event whose FAIL summary line reads "[setup failed]" rather than
// "[build failed]" -- a distinct reason word classifyEvents didn't
// recognize, so it fell through to KILLED instead of INVALID. These
// tests construct that event stream directly so the fix doesn't depend
// on which platform happens to run the test.
func TestClassifyEventsRecognizesBuildFailedMarker(t *testing.T) {
	events := []testEvent{
		{Action: "start"},
		{Action: "output", Output: "FAIL\texample.test/p [build failed]\n"},
		{Action: "fail"},
	}
	verdict, tests := classifyEvents(events)
	if verdict != model.VerdictInvalid {
		t.Fatalf("verdict = %s, want %s", verdict, model.VerdictInvalid)
	}
	if len(tests) != 0 {
		t.Fatalf("tests = %v, want none", tests)
	}
}

// Reproduces, verbatim, the exact scenario reported from a real macOS
// run: go test -json emitting a package-level FAIL summary line reading
// "[setup failed]" (not "[build failed]") for a build-constraint
// exclusion. Before the fix, this fell through to KILLED.
func TestClassifyEventsRecognizesSetupFailedMarker(t *testing.T) {
	events := []testEvent{
		{Action: "start"},
		{Action: "output", Output: "# example.test/runnerfixture\n"},
		{Action: "output", Output: "package example.test/runnerfixture: build constraints exclude all Go files in /tmp/somewhere\n"},
		{Action: "output", Output: "FAIL example.test/runnerfixture [setup failed]\n"},
		{Action: "fail"},
	}
	verdict, tests := classifyEvents(events)
	if verdict != model.VerdictInvalid {
		t.Fatalf("verdict = %s, want %s", verdict, model.VerdictInvalid)
	}
	if len(tests) != 0 {
		t.Fatalf("tests = %v, want none", tests)
	}
}

// The FAIL-summary-line detection matches the general `FAIL <pkg>
// [<reason>]` structure rather than a fixed list of known reason words,
// specifically so a reason word neither of the two tests above knows
// about (a future Go version, or a platform not tested here) is still
// recognized rather than silently falling through to KILLED again.
func TestClassifyEventsRecognizesUnknownReasonWordInFailBracket(t *testing.T) {
	events := []testEvent{
		{Action: "start"},
		{Action: "output", Output: "FAIL\texample.test/p [some future reason nobody has seen yet]\n"},
		{Action: "fail"},
	}
	verdict, _ := classifyEvents(events)
	if verdict != model.VerdictInvalid {
		t.Fatalf("verdict = %s, want %s", verdict, model.VerdictInvalid)
	}
}

// A FAIL bracket line scoped to a specific test (Test != "") must never
// be treated as a package-level setup/build failure marker -- only
// package-level output (Test == "") counts. This guards against a
// legitimate test that logs or asserts on text shaped like a FAIL
// summary line being misclassified as INVALID.
func TestClassifyEventsIgnoresFailBracketScopedToATest(t *testing.T) {
	events := []testEvent{
		{Action: "start"},
		{Action: "run", Test: "TestWeird"},
		{Action: "output", Test: "TestWeird", Output: "FAIL\tsomething [build failed]\n"},
		{Action: "fail", Test: "TestWeird"},
	}
	verdict, tests := classifyEvents(events)
	if verdict != model.VerdictKilled {
		t.Fatalf("verdict = %s, want %s", verdict, model.VerdictKilled)
	}
	if len(tests) != 1 || tests[0] != "TestWeird" {
		t.Fatalf("tests = %v, want [TestWeird]", tests)
	}
}

func TestTrimOutputPreservesUTF8(t *testing.T) {
	const max = 8
	input := strings.Repeat("a", max-1) + "é" + "tail"
	got := trimOutput(input, max)
	if !utf8.ValidString(got) {
		t.Fatalf("trimmed output is invalid UTF-8: %q", got)
	}
	if !strings.Contains(got, "output truncated") {
		t.Fatalf("missing truncation marker: %q", got)
	}
}

func TestGoTestClassifiesCompileFailureAsInvalid(t *testing.T) {
	root := testModule(t, "package p\nfunc F() int { return missing }\n", "")
	got := (GoTest{}).Run(context.Background(), Request{Root: root, WorkRel: ".", Patterns: []string{"."}, Timeout: 2 * time.Second})
	if got.Verdict != model.VerdictInvalid {
		t.Fatalf("verdict=%s output=%s", got.Verdict, got.Output)
	}
}

func TestGoTestClassifiesRuntimePanicAsKilled(t *testing.T) {
	root := testModule(t, "package p\nfunc F() {}\n", "package p\nimport \"testing\"\nfunc TestPanic(t *testing.T) { panic(\"boom\") }\n")
	got := (GoTest{}).Run(context.Background(), Request{Root: root, WorkRel: ".", Patterns: []string{"."}, Timeout: 2 * time.Second})
	if got.Verdict != model.VerdictKilled {
		t.Fatalf("verdict=%s output=%s", got.Verdict, got.Output)
	}
}

func TestGoTestClassifiesDeadlineAsTimeout(t *testing.T) {
	root := testModule(t, "package p\nfunc F() {}\n", "package p\nimport (\"testing\"; \"time\")\nfunc TestSlow(t *testing.T) { time.Sleep(5*time.Second) }\n")
	got := (GoTest{}).Run(context.Background(), Request{Root: root, WorkRel: ".", Patterns: []string{"."}, Timeout: 50 * time.Millisecond})
	if got.Verdict != model.VerdictTimeout {
		t.Fatalf("verdict=%s output=%s", got.Verdict, got.Output)
	}
}

// A vet failure (not just a compile error) must also classify as
// INVALID: go test always reports it via the same "[build failed]"
// summary line, so it needs no separate pattern.
func TestGoTestClassifiesVetFailureAsInvalid(t *testing.T) {
	root := testModule(t, "package p\nimport \"fmt\"\nfunc F() { fmt.Printf(\"%d\\n\", \"nope\") }\n",
		"package p\nimport \"testing\"\nfunc TestF(t *testing.T) { F() }\n")
	got := (GoTest{}).Run(context.Background(), Request{Root: root, WorkRel: ".", Patterns: []string{"."}, Timeout: 2 * time.Second})
	if got.Verdict != model.VerdictInvalid {
		t.Fatalf("verdict=%s output=%s", got.Verdict, got.Output)
	}
}

// A package that fails before any test can run for a runtime reason --
// here, a panic in init() -- compiled successfully, so the mutant
// produced a genuine failure. It must not be misclassified as INVALID
// just because, like a real build failure, no test ever started.
func TestGoTestClassifiesInitPanicAsKilledNotInvalid(t *testing.T) {
	root := testModule(t, "package p\nfunc init() { panic(\"init boom\") }\nfunc F() {}\n",
		"package p\nimport \"testing\"\nfunc TestF(t *testing.T) {}\n")
	got := (GoTest{}).Run(context.Background(), Request{Root: root, WorkRel: ".", Patterns: []string{"."}, Timeout: 2 * time.Second})
	if got.Verdict != model.VerdictKilled {
		t.Fatalf("verdict=%s output=%s", got.Verdict, got.Output)
	}
}

// A panic in a goroutine the testing package does not directly supervise
// crashes the whole test binary without a clean per-test "fail" event.
// The in-flight test must still be attributed as responsible.
func TestGoTestAttributesAsyncGoroutinePanicToInFlightTest(t *testing.T) {
	root := testModule(t, "package p\nfunc F() {}\n",
		"package p\nimport \"testing\"\nfunc TestAsync(t *testing.T) { go func() { panic(\"async boom\") }(); select {} }\n")
	got := (GoTest{}).Run(context.Background(), Request{Root: root, WorkRel: ".", Patterns: []string{"."}, Timeout: 2 * time.Second})
	if got.Verdict != model.VerdictKilled {
		t.Fatalf("verdict=%s output=%s", got.Verdict, got.Output)
	}
	if len(got.Tests) != 1 || got.Tests[0] != "TestAsync" {
		t.Fatalf("tests=%v, want [TestAsync]", got.Tests)
	}
}

// A package that go test rejects before test2json ever writes JSON (no
// buildable files match the build constraints) must fall back cleanly
// to INVALID rather than being lost or misclassified as UNKNOWN.
func TestGoTestClassifiesExcludedBuildConstraintsAsInvalid(t *testing.T) {
	root := testModule(t, "//go:build never\npackage p\n", "")
	got := (GoTest{}).Run(context.Background(), Request{Root: root, WorkRel: ".", Patterns: []string{"."}, Timeout: 2 * time.Second})
	if got.Verdict != model.VerdictInvalid {
		t.Fatalf("verdict=%s output=%s", got.Verdict, got.Output)
	}
}

func TestGoTestStartFailureIsUnknown(t *testing.T) {
	t.Setenv("PATH", "")
	got := (GoTest{}).Run(context.Background(), Request{Root: t.TempDir(), WorkRel: ".", Patterns: []string{"."}, Timeout: time.Second})
	if got.Verdict != model.VerdictUnknown {
		t.Fatalf("verdict=%s output=%s", got.Verdict, got.Output)
	}
}

func testModule(t *testing.T, source, testSource string) string {
	t.Helper()
	root := t.TempDir()
	files := map[string]string{
		"go.mod": "module example.test/runnerfixture\n\ngo 1.22\n",
		"p.go":   source,
	}
	if testSource != "" {
		files["p_test.go"] = testSource
	}
	for name, data := range files {
		if err := os.WriteFile(filepath.Join(root, name), []byte(data), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root
}
