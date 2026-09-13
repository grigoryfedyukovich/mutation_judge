package gitdiff

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestParse(t *testing.T) {
	d := []byte("diff --git a/parser.go b/parser.go\n--- a/parser.go\n+++ b/parser.go\n@@ -2,0 +3,2 @@\n+x\n+y\n")
	m, err := Parse(d)
	if err != nil {
		t.Fatal(err)
	}
	if !m["parser.go"][3] || !m["parser.go"][4] {
		t.Fatalf("bad lines: %#v", m)
	}
}

func TestParseDeletedFileDoesNotLeakPreviousPath(t *testing.T) {
	d := []byte("diff --git a/a.go b/a.go\n--- a/a.go\n+++ b/a.go\n@@ -1 +1 @@\n-old\n+new\ndiff --git a/deleted.go b/deleted.go\n--- a/deleted.go\n+++ /dev/null\n@@ -1 +0,0 @@\n-gone\n")
	m, err := Parse(d)
	if err != nil {
		t.Fatal(err)
	}
	if !m["a.go"][1] {
		t.Fatalf("missing changed line: %#v", m)
	}
	if _, ok := m["deleted.go"]; ok {
		t.Fatalf("deleted file should not produce current lines: %#v", m)
	}
}

func TestParseZeroCountAndBinaryDiff(t *testing.T) {
	d := []byte("diff --git a/p.go b/p.go\n--- a/p.go\n+++ b/p.go\n@@ -4,0 +5,0 @@\ndiff --git a/blob.go b/blob.go\nBinary files a/blob.go and b/blob.go differ\n")
	m, err := Parse(d)
	if err != nil {
		t.Fatal(err)
	}
	if len(m) != 0 {
		t.Fatalf("unexpected lines: %#v", m)
	}
}

// TestChangedLinesHandlesNonASCIIFilename exercises ChangedLines end to
// end (a real `git diff` subprocess, not just Parse on synthetic bytes,
// since none of the tests above cover ChangedLines' own git
// invocation). Without -c core.quotepath=false, git's default C-quotes
// any non-ASCII byte in the +++ header (`+++ "b/caf\303\251.go"`
// instead of `+++ b/café.go`), which Parse's `b/`-prefix check does not
// recognize -- so a genuine edit to a non-ASCII-named file was silently
// treated as having no changed lines at all under --changed, rather
// than erroring or being included.
func TestChangedLinesHandlesNonASCIIFilename(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	root := t.TempDir()
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = root
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run("init", "-q")
	run("config", "user.email", "a@b.com")
	run("config", "user.name", "test")
	name := "café.go"
	if err := os.WriteFile(filepath.Join(root, name), []byte("package p\n\nfunc F() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run("add", ".")
	run("commit", "-q", "-m", "base")
	if err := os.WriteFile(filepath.Join(root, name), []byte("package p\n\nfunc F() { println(1) }\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	m, err := ChangedLines(root, "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	if !m[name][3] {
		t.Fatalf("expected line 3 of %s to be reported changed, got %#v", name, m)
	}
}
