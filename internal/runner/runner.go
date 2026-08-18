package runner

import (
	"bytes"
	"context"
	"fmt"
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
var buildRE = regexp.MustCompile(`(?m)(\[build failed\]|build constraints exclude all Go files|syntax error:|undefined:|cannot use .* as .* value|declared and not used|expected .* found)`)

func (GoTest) Run(parent context.Context, req Request) Result {
	// Use the same explicit limit for go test and the enclosing process. The
	// process deadline also caps compilation and teardown, which go test's own
	// test-binary timeout does not cover.
	ctx, cancel := context.WithTimeout(parent, req.Timeout)
	defer cancel()
	args := []string{"test", "-count=1", "-timeout", req.Timeout.String()}
	if req.TestRun != "" {
		args = append(args, "-run", req.TestRun)
	}
	if req.CoverageOut != "" {
		args = append(args, "-covermode=count", "-coverprofile="+req.CoverageOut)
	}
	args = append(args, req.Patterns...)
	cmd := exec.CommandContext(ctx, "go", args...)
	cmd.Dir = filepath.Join(req.Root, filepath.FromSlash(req.WorkRel))
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	start := time.Now()
	err := cmd.Run()
	dur := time.Since(start).Milliseconds()
	text := trimOutput(out.String(), 64*1024)
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
	res.Tests = failingTests(text)
	if buildRE.MatchString(text) {
		res.Verdict = model.VerdictInvalid
	} else {
		res.Verdict = model.VerdictKilled
	}
	return res
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
