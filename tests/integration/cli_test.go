package integration

import (
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
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
