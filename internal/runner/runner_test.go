package runner

import (
	"context"
	"errors"
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

func TestFailingTestsIncludesIndentedSubtests(t *testing.T) {
	got := failingTests("    --- FAIL: TestX/sub (0.00s)\n--- FAIL: TestX (0.00s)\n")
	if len(got) != 2 || got[0] != "TestX" || got[1] != "TestX/sub" {
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

// Reproduces the P0 multi-package bug directly at the classifyEvents
// level: patterns like `./...` merge every package's events into one
// stream. Before the fix, a sibling package's tests starting at all
// (Action "run" with Test != "", regardless of which package) set a
// single global sawRun flag that unconditionally suppressed the INVALID
// verdict for a completely unrelated package that failed to build in
// the same run -- inflating the mutation score by counting an
// uncompilable mutant as a kill. The two packages here are deliberately
// unrelated, matching the real reproduction (mutate examples/arithmetic
// under patterns `./...`; examples/boolean's own tests still run).
func TestClassifyEventsPackageBuildFailureIsInvalidEvenWhenSiblingPackageRuns(t *testing.T) {
	events := []testEvent{
		// The mutated package fails to build before any test in it runs.
		{Action: "output", Package: "example.test/broken", Output: "# example.test/broken\n"},
		{Action: "output", Package: "example.test/broken", Output: "./broken.go:3:9: invalid operation\n"},
		{Action: "output", Package: "example.test/broken", Output: "FAIL\texample.test/broken [build failed]\n"},
		{Action: "fail", Package: "example.test/broken"},
		// An unrelated sibling package in the same `./...` pattern set
		// compiles fine and its own test passes.
		{Action: "run", Package: "example.test/sibling", Test: "TestSibling"},
		{Action: "output", Package: "example.test/sibling", Test: "TestSibling", Output: "=== RUN   TestSibling\n"},
		{Action: "pass", Package: "example.test/sibling", Test: "TestSibling"},
		{Action: "pass", Package: "example.test/sibling"},
	}
	verdict, tests := classifyEvents(events)
	if verdict != model.VerdictInvalid {
		t.Fatalf("verdict = %s, want %s (a sibling package's tests running must not mask the mutated package's build failure)", verdict, model.VerdictInvalid)
	}
	if len(tests) != 0 {
		t.Fatalf("tests = %v, want none", tests)
	}
}

// The counterpart to the test above: when the sibling package's test
// actually fails too (not just runs and passes), that is a genuine
// observed failure and must still win as KILLED -- the fix must not
// overcorrect into hiding a real kill behind an unrelated build failure
// elsewhere in the same pattern set.
func TestClassifyEventsRealFailureElsewhereStillWinsOverBuildFailure(t *testing.T) {
	events := []testEvent{
		{Action: "output", Package: "example.test/broken", Output: "FAIL\texample.test/broken [build failed]\n"},
		{Action: "fail", Package: "example.test/broken"},
		{Action: "run", Package: "example.test/sibling", Test: "TestSibling"},
		{Action: "fail", Package: "example.test/sibling", Test: "TestSibling"},
	}
	verdict, tests := classifyEvents(events)
	if verdict != model.VerdictKilled {
		t.Fatalf("verdict = %s, want %s", verdict, model.VerdictKilled)
	}
	if len(tests) != 1 || tests[0] != "TestSibling" {
		t.Fatalf("tests = %v, want [TestSibling]", tests)
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

// The real end-to-end reproduction of the P0 multi-package bug: two
// independent packages under one module, patterns `./...`. The mutated
// package fails to compile; the unrelated sibling package's own test
// runs and passes regardless. Before the fix this was misclassified
// KILLED because *a* test started somewhere in the merged `go test
// -json` stream, even though it was never the mutated package's own
// test. TestGoTestClassifiesCompileFailureAsInvalid above cannot catch
// this: it uses a single-package module, where there is no sibling
// package whose tests could start and mask the bug.
func TestGoTestClassifiesCompileFailureAsInvalidAcrossPackages(t *testing.T) {
	root := t.TempDir()
	files := map[string]string{
		"go.mod":                  "module example.test/multipkg\n\ngo 1.22\n",
		"broken/broken.go":        "package broken\nfunc F() int { return missing }\n",
		"sibling/sibling.go":      "package sibling\nfunc F() int { return 1 }\n",
		"sibling/sibling_test.go": "package sibling\nimport \"testing\"\nfunc TestF(t *testing.T) { if F() != 1 { t.Fatal(\"bad\") } }\n",
	}
	for name, data := range files {
		path := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(data), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	got := (GoTest{}).Run(context.Background(), Request{Root: root, WorkRel: ".", Patterns: []string{"./..."}, Timeout: 5 * time.Second})
	if got.Verdict != model.VerdictInvalid {
		t.Fatalf("verdict=%s (want %s) output=%s", got.Verdict, model.VerdictInvalid, got.Output)
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

// isTimeout is the pure decision function behind TIMEOUT classification
// (see its own doc comment for the two real, found bugs this fixes),
// tested directly with synthetic inputs since reliably forcing the real
// race it's meant to catch -- go test's own internal -timeout firing
// and self-terminating the process before the outer context's identical
// deadline does -- is inherently timing-dependent and not something a
// fast, deterministic unit test should depend on. The genuine panic
// text below is copied verbatim from real `go test -timeout` output
// shape, and the two "must not match" cases are drawn directly from the
// bug reports: a passing suite and a genuinely failing, unrelated test
// that each merely contain similar-looking phrasing in their own
// output.
func TestIsTimeout(t *testing.T) {
	failed := errors.New("exit status 1")
	cases := []struct {
		name        string
		ctxErr, err error
		text        string
		want        bool
	}{
		{
			name:   "outer context deadline is definitive regardless of output",
			ctxErr: context.DeadlineExceeded,
			want:   true,
		},
		{
			name: "go test's own timeout panic header on a failed process is a timeout",
			err:  failed,
			text: "panic: test timed out after 30s\nrunning tests:\n\tTestSlow (30s)\n\ngoroutine 1 [running]:\ntesting.(*M).startAlarm.func1()\nFAIL\texample.com/pkg\t30.006s\n",
			want: true,
		},
		{
			name:   "outer context deadline still wins even alongside a nil err",
			ctxErr: context.DeadlineExceeded,
			err:    nil,
			want:   true,
		},
		{
			name: "a passing suite (err == nil) whose own output merely contains similar phrasing must not be a timeout",
			err:  nil,
			text: "=== RUN   TestReportsTimeoutCorrectly\nthis suite verifies the message: test timed out after 30s\n--- PASS: TestReportsTimeoutCorrectly (0.00s)\nPASS\n",
			want: false,
		},
		{
			name: "a real failure whose own message merely contains similar phrasing, not go test's own panic header, must still be a real kill",
			err:  failed,
			text: "--- FAIL: TestRetry (0.00s)\n    retry_test.go:12: expected retry to succeed, got: test timed out after 3 attempts\nFAIL\n",
			want: false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := isTimeout(tc.ctxErr, tc.err, tc.text); got != tc.want {
				t.Fatalf("isTimeout() = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestGoTestDoesNotClassifyPassingSuiteMentioningTimeoutPhraseAsTimeout
// is the end-to-end reproduction of the first real bug (not just the
// isTimeout unit test above): a genuinely passing test (err == nil, no
// outer deadline) that prints -- via fmt.Println directly to the
// process's own stdout, so it reaches the captured output regardless
// of go test's per-test log buffering -- a message that happens to
// contain "test timed out after", exactly the kind of thing a suite
// that specifically exercises timeout-handling code would legitimately
// do. If this were the baseline run, misclassifying it TIMEOUT would
// abort the whole analysis outright.
func TestGoTestDoesNotClassifyPassingSuiteMentioningTimeoutPhraseAsTimeout(t *testing.T) {
	root := testModule(t, "package p\nfunc F() string { return \"ok\" }\n",
		"package p\nimport (\"fmt\"; \"testing\")\nfunc TestF(t *testing.T) {\n\tfmt.Println(\"this suite verifies the message: test timed out after 30s\")\n\tif F() != \"ok\" {\n\t\tt.Fatal(\"bad\")\n\t}\n}\n")
	got := (GoTest{}).Run(context.Background(), Request{Root: root, WorkRel: ".", Patterns: []string{"."}, Timeout: 5 * time.Second})
	if got.Verdict != model.VerdictSurvived {
		t.Fatalf("verdict=%s (want %s) output=%s", got.Verdict, model.VerdictSurvived, got.Output)
	}
}

// TestGoTestDoesNotClassifyUnrelatedFailureMentioningTimeoutPhraseAsTimeout
// is the end-to-end reproduction of the second real bug: a genuinely
// failing test (a real kill: err != nil, a normal t.Fatalf, no outer
// deadline, no actual go-test-internal timeout) whose own failure
// message happens to contain "test timed out after" as an unrelated
// substring. This must classify as a real kill with the failing test
// named, not silently vanish from the score as a TIMEOUT.
func TestGoTestDoesNotClassifyUnrelatedFailureMentioningTimeoutPhraseAsTimeout(t *testing.T) {
	root := testModule(t, "package p\nfunc F() bool { return false }\n",
		"package p\nimport \"testing\"\nfunc TestF(t *testing.T) {\n\tif !F() {\n\t\tt.Fatalf(\"expected retry to succeed, got: test timed out after 3 attempts\")\n\t}\n}\n")
	got := (GoTest{}).Run(context.Background(), Request{Root: root, WorkRel: ".", Patterns: []string{"."}, Timeout: 5 * time.Second})
	if got.Verdict != model.VerdictKilled {
		t.Fatalf("verdict=%s (want %s) output=%s", got.Verdict, model.VerdictKilled, got.Output)
	}
	if len(got.Tests) != 1 || got.Tests[0] != "TestF" {
		t.Fatalf("Tests = %v, want [TestF]", got.Tests)
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

// DetectToolchain and GoTest.Version must read the invoked go
// toolchain itself (`go env GOVERSION`, matching what `go test` will
// actually build and run), never runtime.Version() -- the toolchain
// that happened to compile this mutation-judge binary, which can
// silently disagree with it (a cross-compiled CLI, or an
// upgraded/downgraded `go` on PATH since the binary was built). This
// is the direct fix for the P0 cache-key bug (see
// TestCacheKeySensitiveToGoToolchainChange in internal/analysis for the
// end-to-end regression); these two tests pin the primitive itself.
func TestClassifyEventsSameTestNameInTwoPackagesDoesNotCancelInFlight(t *testing.T) {
	events := []testEvent{
		{Action: "run", Package: "example.test/a", Test: "TestFoo"},
		{Action: "run", Package: "example.test/b", Test: "TestFoo"},
		{Action: "pass", Package: "example.test/a", Test: "TestFoo"},
		// b's TestFoo started and never resolved: in-flight crash.
	}
	verdict, tests := classifyEvents(events)
	if verdict != model.VerdictKilled {
		t.Fatalf("verdict = %s, want %s", verdict, model.VerdictKilled)
	}
	if len(tests) != 1 || tests[0] != "TestFoo" {
		t.Fatalf("tests = %v, want [TestFoo] attributed from package b", tests)
	}
}

func TestParseGoEnvKeepsEmptyGoflags(t *testing.T) {
	got, err := parseGoEnv([]byte("go1.22.10\nlinux\namd64\n1\n\n"))
	if err != nil {
		t.Fatal(err)
	}
	want := ToolchainInfo{GoVersion: "go1.22.10", GOOS: "linux", GOARCH: "amd64", CgoEnabled: "1", GoFlags: ""}
	if got != want {
		t.Fatalf("got %+v, want %+v", got, want)
	}
}

func TestParseGoEnvAcceptsCRLFAndNonEmptyGoflags(t *testing.T) {
	got, err := parseGoEnv([]byte("go1.23.4\r\nwindows\r\namd64\r\n0\r\n-race\r\n"))
	if err != nil {
		t.Fatal(err)
	}
	want := ToolchainInfo{GoVersion: "go1.23.4", GOOS: "windows", GOARCH: "amd64", CgoEnabled: "0", GoFlags: "-race"}
	if got != want {
		t.Fatalf("got %+v, want %+v", got, want)
	}
}

func TestParseGoEnvRejectsTruncatedOutput(t *testing.T) {
	if _, err := parseGoEnv([]byte("go1.22.10\nlinux\namd64\n1")); err == nil {
		t.Fatal("expected error for 4 lines without a trailing GOFLAGS field")
	}
}

func TestDetectToolchainReadsInvokedGoNotRuntimeVersion(t *testing.T) {
	tc, err := DetectToolchain(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(tc.GoVersion, "go") {
		t.Fatalf("GoVersion = %q, want a `go env GOVERSION`-shaped value (e.g. go1.23.4)", tc.GoVersion)
	}
	if tc.GOOS == "" || tc.GOARCH == "" {
		t.Fatalf("GOOS/GOARCH must not be empty: %+v", tc)
	}
	if tc.CgoEnabled != "0" && tc.CgoEnabled != "1" {
		t.Fatalf("CgoEnabled = %q, want \"0\" or \"1\"", tc.CgoEnabled)
	}
}

func TestDetectToolchainMissingGoIsAnError(t *testing.T) {
	t.Setenv("PATH", "")
	if _, err := DetectToolchain(context.Background()); err == nil {
		t.Fatal("expected an error with no go on PATH, got nil")
	}
}

func TestGoTestVersionMissingGoIsUnknownNotRuntimeVersion(t *testing.T) {
	t.Setenv("PATH", "")
	if got := (GoTest{}).Version(); got != "unknown" {
		t.Fatalf("Version() = %q, want the distinct sentinel %q (never a silent fallback to runtime.Version())", got, "unknown")
	}
}

func TestToolchainInfoKeyDistinguishesEveryField(t *testing.T) {
	base := ToolchainInfo{GoVersion: "go1.23.4", GOOS: "linux", GOARCH: "amd64", CgoEnabled: "0", GoFlags: ""}
	variants := []ToolchainInfo{
		{GoVersion: "go1.24.0", GOOS: base.GOOS, GOARCH: base.GOARCH, CgoEnabled: base.CgoEnabled, GoFlags: base.GoFlags},
		{GoVersion: base.GoVersion, GOOS: "darwin", GOARCH: base.GOARCH, CgoEnabled: base.CgoEnabled, GoFlags: base.GoFlags},
		{GoVersion: base.GoVersion, GOOS: base.GOOS, GOARCH: "arm64", CgoEnabled: base.CgoEnabled, GoFlags: base.GoFlags},
		{GoVersion: base.GoVersion, GOOS: base.GOOS, GOARCH: base.GOARCH, CgoEnabled: "1", GoFlags: base.GoFlags},
		{GoVersion: base.GoVersion, GOOS: base.GOOS, GOARCH: base.GOARCH, CgoEnabled: base.CgoEnabled, GoFlags: "-race"},
	}
	baseKey := base.Key()
	for i, v := range variants {
		if v.Key() == baseKey {
			t.Fatalf("variant %d (%+v) produced the same key as base %+v: %s", i, v, base, baseKey)
		}
	}
	if base.Key() != (ToolchainInfo{GoVersion: base.GoVersion, GOOS: base.GOOS, GOARCH: base.GOARCH, CgoEnabled: base.CgoEnabled, GoFlags: base.GoFlags}).Key() {
		t.Fatal("identical ToolchainInfo values produced different keys")
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
