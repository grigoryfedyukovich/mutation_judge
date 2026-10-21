package analysis

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/example/mutation-judge/internal/config"
	"github.com/example/mutation-judge/internal/runner"
)

func TestR03HostEditsAfterCaptureCannotChangePackageDiscovery(t *testing.T) {
	root := testProject(t, "package p\nfunc F(x int) bool { return x > 0 }\n")
	cfg := config.Default()
	cfg.Cache = false
	cfg.Operators = []string{"boundary"}
	cfg.NarrowTestScope = true
	engine := Engine{afterSnapshot: func() {
		if err := os.Remove(filepath.Join(root, "p.go")); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, "q.go"), []byte("package p\nfunc New(x int) bool{return x > 5}\n"), 0644); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("this is not a module\n"), 0644); err != nil {
			t.Fatal(err)
		}
	}}
	p, err := engine.prepare(Request{CWD: root, Patterns: []string{"."}, Config: cfg}, runner.ToolchainInfo{GoVersion: "go1.99"})
	if err != nil {
		t.Fatalf("preparation must use frozen module, not modified host: %v", err)
	}
	defer p.cleanup()
	if len(p.mutants) != 1 || p.mutants[0].Span.File != "p.go" {
		t.Fatalf("mutants from mutable host, not snapshot: %+v", p.mutants)
	}
	if _, ok := p.filePackage["q.go"]; ok {
		t.Fatal("new host file leaked into package list")
	}
}

func gitCmdR03(t *testing.T, cwd string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = cwd
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v failed: %v\n%s", args, err, out)
	}
}

func TestR03GitDiffUsesFrozenSourceNotEditedHost(t *testing.T) {
	root := testProject(t, "package p\nfunc F(x int) bool { return x > 0 }\n")
	gitCmdR03(t, root, "init", "-q")
	gitCmdR03(t, root, "-c", "user.name=Test", "-c", "user.email=t@example.com", "add", ".")
	gitCmdR03(t, root, "-c", "user.name=Test", "-c", "user.email=t@example.com", "commit", "-qm", "initial")
	if err := os.WriteFile(filepath.Join(root, "p.go"), []byte("package p\nfunc F(x int) bool { return x > 1 }\n"), 0644); err != nil {
		t.Fatal(err)
	}
	cfg := config.Default()
	cfg.Cache = false
	cfg.ChangedBase = "HEAD"
	cfg.Operators = []string{"boundary"}
	engine := Engine{afterSnapshot: func() {
		// Reverse the tracked change and introduce a new, untracked mutant.
		if err := os.WriteFile(filepath.Join(root, "p.go"), []byte("package p\nfunc F(x int) bool { return x > 0 }\n"), 0644); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, "q.go"), []byte("package p\nfunc Q(x int) bool { return x > 3 }\n"), 0644); err != nil {
			t.Fatal(err)
		}
	}}
	p, err := engine.prepare(Request{CWD: root, Patterns: []string{"."}, Config: cfg}, runner.ToolchainInfo{GoVersion: "go1.99"})
	if err != nil {
		t.Fatal(err)
	}
	defer p.cleanup()
	if len(p.mutants) != 1 || p.mutants[0].Span.File != "p.go" {
		t.Fatalf("Git selection read host changes: %+v", p.mutants)
	}
	b, err := os.ReadFile(filepath.Join(p.sandbox, "p.go"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), "x > 1") {
		t.Fatalf("snapshot changed after capture: %s", b)
	}
}
