package runner

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/example/mutation-judge/internal/model"
)

type Request struct {
	Root        string
	WorkRel     string
	Patterns    []string
	TestRun     string
	Timeout     time.Duration
	CoverageOut string
}

type Result struct {
	Verdict    model.Verdict `json:"verdict"`
	Output     string        `json:"output"`
	Tests      []string      `json:"tests,omitempty"`
	DurationMS int64         `json:"duration_ms"`
	ExitCode   int           `json:"exit_code"`
	GoVersion  string        `json:"go_version"`
}

type Backend interface {
	Run(context.Context, Request) Result
}

type DescribedBackend interface {
	Backend
	Name() string
	Version() string
}

type GoTest struct{}

func (GoTest) Name() string    { return "go-test" }
func (GoTest) Version() string { return runtime.Version() }

var failRE = regexp.MustCompile(`(?m)^--- FAIL: ([^ (]+)`)

// fallbackBuildFailureRE is a last-resort heuristic used only when `go
// test -json` produces no decodable event stream at all (for example,
// "build constraints exclude all Go files", which `go test` rejects
// before test2json ever starts writing JSON). Every ordinary build or
// vet failure is instead detected from the tool's own "[build failed]"
// protocol marker in the JSON event stream -- see classifyEvents -- which
// does not depend on guessing at the wording of a specific compiler
// diagnostic.
var fallbackBuildFailureRE = regexp.MustCompile(`(?m)(\[build failed\]|build constraints exclude all Go files|syntax error:|undefined:|cannot use .* as .* value|declared and not used|expected .* found)`)

// testEvent mirrors one line of the line-delimited JSON that `go test
// -json` writes to stdout (via cmd/internal/test2json). stdout carries
// only this JSON stream; raw compiler/vet diagnostics and any
// out-of-band failures (e.g. build-constraint exclusion) go to stderr
// and never interleave with it, even when the build itself fails.
type testEvent struct {
	Action  string  `json:"Action"`
	Test    string  `json:"Test"`
	Output  string  `json:"Output"`
	Elapsed float64 `json:"Elapsed"`
}

func (GoTest) Run(parent context.Context, req Request) Result {
	// Use the same explicit limit for go test and the enclosing process. The
	// process deadline also caps compilation and teardown, which go test's own
	// test-binary timeout does not cover.
	ctx, cancel := context.WithTimeout(parent, req.Timeout)
	defer cancel()
	args := []string{"test", "-json", "-count=1", "-timeout", req.Timeout.String()}
	if req.TestRun != "" {
		args = append(args, "-run", req.TestRun)
	}
	if req.CoverageOut != "" {
		args = append(args, "-covermode=count", "-coverprofile="+req.CoverageOut)
	}
	args = append(args, req.Patterns...)
	cmd := exec.CommandContext(ctx, "go", args...)
	cmd.Dir = filepath.Join(req.Root, filepath.FromSlash(req.WorkRel))
	// Captured separately: jsonOut is decoded as structured events; errOut
	// is kept only to surface human-readable diagnostics (e.g. the actual
	// compiler error line) in the report and as fallback evidence.
	var jsonOut, errOut bytes.Buffer
	cmd.Stdout = &jsonOut
	cmd.Stderr = &errOut
	start := time.Now()
	err := cmd.Run()
	dur := time.Since(start).Milliseconds()

	events, decodeErr := decodeTestEvents(jsonOut.Bytes())
	text := trimOutput(reconstructOutput(errOut.String(), events), 64*1024)
	res := Result{DurationMS: dur, Output: text, GoVersion: runtime.Version()}

	if ctx.Err() == context.DeadlineExceeded || strings.Contains(text, "test timed out after") {
		res.Verdict = model.VerdictTimeout
		res.ExitCode = -1
		return res
	}
	if parent.Err() != nil {
		res.Verdict = model.VerdictUnknown
		res.ExitCode = -1
		return res
	}
	if err == nil {
		res.Verdict = model.VerdictSurvived
		return res
	}
	if ee, ok := err.(*exec.ExitError); ok {
		res.ExitCode = ee.ExitCode()
	} else {
		res.ExitCode = -1
		res.Verdict = model.VerdictUnknown
		return res
	}

	if decodeErr != nil || len(events) == 0 {
		// go test did not produce a parseable -json stream at all: an
		// unusual toolchain failure that never reaches test2json (for
		// example, a build-constraint exclusion is rejected during
		// package loading, before any JSON is written). Fall back to the
		// evidence we still have.
		res.Tests = failingTests(text)
		if fallbackBuildFailureRE.MatchString(text) {
			res.Verdict = model.VerdictInvalid
		} else {
			res.Verdict = model.VerdictKilled
		}
		return res
	}

	res.Verdict, res.Tests = classifyEvents(events)
	return res
}

// decodeTestEvents streams the line-delimited JSON objects `go test
// -json` writes to stdout. It returns whatever it successfully decoded
// even if a later line fails to parse, alongside that error, so a
// truncated stream (e.g. the process was killed mid-write) still yields
// partial, useful evidence to the caller.
func decodeTestEvents(data []byte) ([]testEvent, error) {
	if len(bytes.TrimSpace(data)) == 0 {
		return nil, nil
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	var events []testEvent
	for {
		var e testEvent
		err := dec.Decode(&e)
		if err == io.EOF {
			return events, nil
		}
		if err != nil {
			return events, fmt.Errorf("decode go test -json output: %w", err)
		}
		events = append(events, e)
	}
}

// classifyEvents turns a decoded -json event stream into a verdict and a
// responsible-tests list, using the tool's own event protocol rather than
// matching substrings of English compiler output:
//
//   - A package-level "output" event containing the literal "[build
//     failed]" marker is exactly the FAIL summary line `go test` itself
//     always emits for a build or vet failure, regardless of which
//     specific diagnostic caused it. Its presence, combined with no test
//     ever having started, means INVALID.
//   - If no test ever started and that marker is absent, the package
//     still failed before any test ran, but for a runtime reason (a
//     package-level init() panic, a TestMain that calls os.Exit, and
//     similar) rather than a compile/vet failure: the mutant compiled
//     and produced a real failure, so this is KILLED with no specific
//     test attributed.
//   - Otherwise at least one test started; every test that reported
//     "fail" is responsible. A test that started but never resolved
//     before the stream ended was in flight when the process crashed
//     (for example an unrecovered panic in a goroutine the testing
//     package doesn't directly supervise, which aborts the whole binary
//     without emitting a clean per-test fail event) and is attributed as
//     responsible too.
func classifyEvents(events []testEvent) (model.Verdict, []string) {
	started := map[string]bool{}
	failed := map[string]bool{}
	var order []string
	sawRun := false
	buildFailed := false
	for _, e := range events {
		switch e.Action {
		case "run":
			if e.Test != "" {
				sawRun = true
				started[e.Test] = true
			}
		case "pass", "skip":
			if e.Test != "" {
				delete(started, e.Test)
			}
		case "fail":
			if e.Test != "" {
				delete(started, e.Test)
				if !failed[e.Test] {
					failed[e.Test] = true
					order = append(order, e.Test)
				}
			}
		case "output":
			if e.Test == "" && strings.Contains(e.Output, "[build failed]") {
				buildFailed = true
			}
		}
	}
	for test := range started {
		if !failed[test] {
			failed[test] = true
			order = append(order, test)
		}
	}
	sort.Strings(order)
	if !sawRun {
		if buildFailed {
			return model.VerdictInvalid, nil
		}
		return model.VerdictKilled, nil
	}
	return model.VerdictKilled, order
}

// reconstructOutput rebuilds a single human-readable text block from the
// separately captured stderr (compiler/vet diagnostics, if any) and the
// ordered "Output" fields of the decoded stdout events, approximating
// what an unmerged `go test` (without -json) invocation would have
// printed.
func reconstructOutput(stderr string, events []testEvent) string {
	var b strings.Builder
	b.WriteString(stderr)
	for _, e := range events {
		b.WriteString(e.Output)
	}
	return b.String()
}

func failingTests(out string) []string {
	matches := failRE.FindAllStringSubmatch(out, -1)
	seen := map[string]bool{}
	var tests []string
	for _, m := range matches {
		if len(m) > 1 && !seen[m[1]] {
			seen[m[1]] = true
			tests = append(tests, m[1])
		}
	}
	sort.Strings(tests)
	return tests
}

func trimOutput(s string, max int) string {
	if len(s) <= max {
		return strings.TrimSpace(s)
	}
	end := max
	for end > 0 && !utf8.ValidString(s[:end]) {
		end--
	}
	return fmt.Sprintf("%s\n... output truncated (%d bytes total)", strings.TrimSpace(s[:end]), len(s))
}
