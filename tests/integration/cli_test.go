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
