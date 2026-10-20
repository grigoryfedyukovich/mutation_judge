package analysis

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/example/mutation-judge/internal/config"
	"github.com/example/mutation-judge/internal/model"
	"github.com/example/mutation-judge/internal/runner"
)

func TestIncrementalDiscoversUntrackedSource(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git unavailable")
	}
	dir := testProject(t, "package p\nfunc F(x int) bool { return x > 0 }\n")
	if err := os.WriteFile(filepath.Join(dir, "p_test.go"), []byte("package p\nimport \"testing\"\nfunc TestWeak(t *testing.T) { if !F(1) { t.Fatal(\"fail\") } }\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	git := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
	}
	git("init", "-q")
	git("config", "user.name", "test")
	git("config", "user.email", "test@example.com")
	git("add", "go.mod", "p_test.go")
	git("commit", "-qm", "base")
	cfg := config.Default()
	cfg.Cache = false
	cfg.Operators = []string{"boundary"}
	cfg.ChangedBase = "HEAD"
	engine := Engine{Version: "test", Backend: &contentAwareFakeBackend{relPath: "p.go", verdicts: map[string]runner.Result{
		">= 0": {Verdict: model.VerdictKilled},
	}}}
	report, err := engine.Analyze(context.Background(), Request{CWD: dir, Patterns: []string{"."}, Config: cfg})
	if err != nil {
		t.Fatal(err)
	}
	if !report.Complete || len(report.Results) != 1 || report.Results[0].Verdict != model.VerdictKilled {
		t.Fatalf("untracked source omitted: complete=%v results=%#v", report.Complete, report.Results)
	}
	// A new directory is included for ./... but not for the root-only
	// pattern. Incremental discovery must honor Go package selection.
	if err := os.Mkdir(filepath.Join(dir, "newpkg"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "newpkg", "new.go"), []byte("package newpkg\nfunc G(x int) bool { return x > 0 }\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	all, err := engine.Analyze(context.Background(), Request{CWD: dir, Patterns: []string{"./..."}, Config: cfg})
	if err != nil {
		t.Fatal(err)
	}
	if !all.Complete || len(all.Results) != 2 {
		t.Fatalf("untracked new package omitted: complete=%v results=%#v", all.Complete, all.Results)
	}
	rootOnly, err := engine.Analyze(context.Background(), Request{CWD: dir, Patterns: []string{"."}, Config: cfg})
	if err != nil {
		t.Fatal(err)
	}
	if len(rootOnly.Results) != 1 {
		t.Fatalf("package selection leaked new subpackage: %#v", rootOnly.Results)
	}
}
