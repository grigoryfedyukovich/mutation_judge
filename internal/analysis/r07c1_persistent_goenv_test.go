package analysis

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/example/mutation-judge/internal/config"
	"github.com/example/mutation-judge/internal/model"
	"github.com/example/mutation-judge/internal/runner"
)

// R07-C1: persistent go env -w configuration is not inherited by os.Environ.
// Changing CGO_CFLAGS under one unchanged GOENV path must change cache
// identity, despite unchanged Go sources, exported environment and flags.
func TestR07C1PersistentGoConfigInvalidatesCachedMutant(t *testing.T) {
	enabled, err := exec.Command("go", "env", "CGO_ENABLED").Output()
	if err != nil || strings.TrimSpace(string(enabled)) != "1" {
		t.Skip("requires cgo-enabled Go toolchain")
	}
	project := t.TempDir()
	goenv := filepath.Join(t.TempDir(), "goenv")
	t.Setenv("GOENV", goenv)
	t.Setenv("GOFLAGS", "-buildvcs=false")
	for name, contents := range map[string]string{
		"go.mod": "module example.test/r07c1\n\ngo 1.22\n",
		"p.go": `package p
/*
#ifdef MJ_STRICT
int strict() { return 1; }
#else
int strict() { return 0; }
#endif
*/
import "C"
func F(x int) bool { return x > 0 }
func Strict() bool { return C.strict() != 0 }
`,
		"p_test.go": `package p
import "testing"
func TestBoundary(t *testing.T) {
    if !F(1) { t.Fatal("positive") }
    if F(0) && Strict() { t.Fatal("zero boundary should be false") }
}
`,
	} {
		if err := os.WriteFile(filepath.Join(project, name), []byte(contents), 0644); err != nil {
			t.Fatal(err)
		}
	}
	cfg := config.Default()
	cfg.Operators = []string{"boundary"}
	cfg.CacheDir = filepath.Join(project, ".mutation-judge", "cache")
	run := func() model.Result {
		t.Helper()
		report, err := (Engine{Version: "test", Backend: runner.GoTest{}}).Analyze(context.Background(), Request{CWD: project, Patterns: []string{"."}, Config: cfg})
		if err != nil {
			t.Fatal(err)
		}
		if len(report.Results) != 1 {
			t.Fatalf("mutants: got %d, want 1", len(report.Results))
		}
		return report.Results[0]
	}
	if err := os.WriteFile(goenv, []byte("CGO_CFLAGS=-DMJ_NOT_STRICT\n"), 0600); err != nil {
		t.Fatal(err)
	}
	before := run()
	if before.Cached || before.Verdict != model.VerdictSurvived {
		t.Fatalf("producer: cached=%v verdict=%s; want fresh SURVIVED", before.Cached, before.Verdict)
	}
	if err := os.WriteFile(goenv, []byte("CGO_CFLAGS=-DMJ_STRICT\n"), 0600); err != nil {
		t.Fatal(err)
	}
	after := run()
	if after.Cached || after.Verdict != model.VerdictKilled {
		t.Fatalf("consumer: cached=%v verdict=%s; want fresh KILLED", after.Cached, after.Verdict)
	}
	repeated := run()
	if !repeated.Cached || repeated.Verdict != model.VerdictKilled {
		t.Fatalf("repeat: cached=%v verdict=%s; want cached KILLED", repeated.Cached, repeated.Verdict)
	}
}
