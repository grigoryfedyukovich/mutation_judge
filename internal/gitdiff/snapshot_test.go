package gitdiff

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/example/mutation-judge/internal/workspace"
)

func gitFixtureCmd(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

func TestR03FrozenDiffInNestedGoModule(t *testing.T) {
	repo := t.TempDir()
	module := filepath.Join(repo, "inner")
	if err := os.MkdirAll(module, 0755); err != nil {
		t.Fatal(err)
	}
	write := func(rel, data string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(module, rel), []byte(data), 0644); err != nil {
			t.Fatal(err)
		}
	}
	gitFixtureCmd(t, repo, "init", "-q")
	write("go.mod", "module example.org/inner\n\ngo 1.22\n")
	write("a.go", "package inner\nfunc F(x int)bool{return x>0}\n")
	gitFixtureCmd(t, repo, "add", ".")
	gitFixtureCmd(t, repo, "-c", "user.name=T", "-c", "user.email=t@e", "commit", "-qm", "first")
	write("a.go", "package inner\nfunc F(x int)bool{return x>=0}\n")
	write("new.go", "package inner\nfunc G(x int)bool{return x>1}\n")
	base, err := CaptureSnapshotBase(context.Background(), module, "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	defer base.Close()
	snap, cleanup, err := workspace.CopyModule(module, "")
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	// Changing the host after the snapshot must not affect either diff kind.
	write("a.go", "package inner\nfunc F(x int)bool{return x>0}\n")
	if err := os.Remove(filepath.Join(module, "new.go")); err != nil {
		t.Fatal(err)
	}
	lines, err := base.ChangedLinesFromSnapshotContext(context.Background(), snap)
	if err != nil {
		t.Fatal(err)
	}
	if !lines["a.go"][2] || !lines["new.go"][0] {
		t.Fatalf("incorrect frozen changed-lines selection: %#v", lines)
	}
	// Reusing the pinned Git state does not reuse a mutable Git worktree.
	again, err := base.ChangedLinesFromSnapshotContext(context.Background(), snap)
	if err != nil || !again["a.go"][2] || !again["new.go"][0] {
		t.Fatalf("repeated frozen diff failed: %v, %#v", err, again)
	}
}

func TestR03GitIndexMutationRejected(t *testing.T) {
	repo := t.TempDir()
	gitFixtureCmd(t, repo, "init", "-q")
	if err := os.WriteFile(filepath.Join(repo, "a.go"), []byte("package p\n"), 0644); err != nil {
		t.Fatal(err)
	}
	gitFixtureCmd(t, repo, "add", ".")
	gitFixtureCmd(t, repo, "-c", "user.name=T", "-c", "user.email=t@e", "commit", "-qm", "first")
	base, err := CaptureSnapshotBase(context.Background(), repo, "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	defer base.Close()
	if err := os.WriteFile(filepath.Join(repo, "b.go"), []byte("package p\n"), 0644); err != nil {
		t.Fatal(err)
	}
	gitFixtureCmd(t, repo, "add", "b.go")
	if err := base.VerifyIndex(); err == nil {
		t.Fatal("index change not detected")
	}
}

func TestR03NestedModuleInheritsFrozenParentGitignore(t *testing.T) {
	repo := t.TempDir()
	module := filepath.Join(repo, "inner")
	if err := os.MkdirAll(module, 0755); err != nil {
		t.Fatal(err)
	}
	gitFixtureCmd(t, repo, "init", "-q")
	if err := os.WriteFile(filepath.Join(repo, ".gitignore"), []byte("*.skip.go\n"), 0644); err != nil {
		t.Fatal(err)
	}
	write := func(name, code string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(module, name), []byte(code), 0644); err != nil {
			t.Fatal(err)
		}
	}
	write("a.go", "package inner\n")
	write("go.mod", "module example.org/inner\n\ngo 1.22\n")
	gitFixtureCmd(t, repo, "add", ".")
	gitFixtureCmd(t, repo, "-c", "user.name=T", "-c", "user.email=t@e", "commit", "-qm", "first")
	write("ignored.skip.go", "package inner\nfunc Ignored(x int)bool{return x>0}\n")
	write("untracked.go", "package inner\nfunc Included(x int)bool{return x>0}\n")
	base, err := CaptureSnapshotBase(context.Background(), module, "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	defer base.Close()
	snap, cleanup, err := workspace.CopyModule(module, "")
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	lines, err := base.ChangedLinesFromSnapshotContext(context.Background(), snap)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := lines["ignored.skip.go"]; ok {
		t.Fatalf("parent ignore lost: %#v", lines)
	}
	if !lines["untracked.go"][0] {
		t.Fatalf("nonignored untracked file missing: %#v", lines)
	}
}
