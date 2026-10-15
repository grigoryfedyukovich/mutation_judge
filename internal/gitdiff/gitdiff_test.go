package gitdiff

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
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

func TestParseQuotedGitPathsAndHunkPayload(t *testing.T) {
	// Octal-quoted UTF-8, escaped quotes/backslashes, and a source line
	// whose payload looks exactly like a +++ file header.
	for _, tc := range []struct{ header, name string }{
		{`"b/caf\303\251.go"`, "café.go"},
		{`"b/quote\".go"`, `quote".go`},
		{`"b/back\\slash.go"`, `back\slash.go`},
		{`"b/tab\tname.go"`, "tab\tname.go"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			old := strings.Replace(tc.header, "b/", "a/", 1)
			d := fmt.Sprintf("diff --git a/p.go b/p.go\n--- %s\n+++ %s\n@@ -1 +1,2 @@\n-old\n+++ b/fake.go\n+next\n", old, tc.header)
			m, err := Parse([]byte(d))
			if err != nil {
				t.Fatal(err)
			}
			if len(m) != 1 || !m[tc.name][1] || !m[tc.name][2] {
				t.Fatalf("want two lines for %q only, got %#v", tc.name, m)
			}
		})
	}
}

func TestParseRejectsUnknownAndMalformedDiffs(t *testing.T) {
	for _, tc := range []struct{ name, data string }{
		{"color", "\x1b[1mdiff --git a/p.go b/p.go\x1b[m\n--- a/p.go\n+++ b/p.go\n@@ -1 +1 @@\n-old\n+new\n"},
		{"bad quoted header", "diff --git a/p.go b/p.go\n--- a/p.go\n+++ \"b/bad\\q.go\"\n@@ -1 +1 @@\n-old\n+new\n"},
		{"unexpected destination prefix", "diff --git a/p.go b/p.go\n--- a/p.go\n+++ c/p.go\n@@ -1 +1 @@\n-old\n+new\n"},
		{"missing destination", "diff --git a/p.go b/p.go\n--- a/p.go\n@@ -1 +1 @@\n-old\n+new\n"},
		{"truncated hunk", "diff --git a/p.go b/p.go\n--- a/p.go\n+++ b/p.go\n@@ -1 +1 @@\n-old\n"},
		{"invalid hunk", "diff --git a/p.go b/p.go\n--- a/p.go\n+++ b/p.go\n@@@ -1 +1 @@@\n-old\n+new\n"},
		{"unknown metadata", "diff --git a/p.go b/p.go\n--- a/p.go\n+++ b/p.go\nSomething invalid\n@@ -1 +1 @@\n-old\n+new\n"},
		{"body count mismatch", "diff --git a/p.go b/p.go\n--- a/p.go\n+++ b/p.go\n@@ -1 +1 @@\n-old\n+new\n+extra\n"},
		{"header only", "diff --git a/p.go b/p.go\n"},
		{"index only", "diff --git a/p.go b/p.go\nindex aaaaaaa..bbbbbbb 100644\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if result, err := Parse([]byte(tc.data)); err == nil {
				t.Fatalf("malformed diff was accepted as %#v", result)
			}
		})
	}
	m, err := Parse(nil)
	if err != nil || len(m) != 0 {
		t.Fatalf("empty diff must succeed: %#v %v", m, err)
	}
}

func TestChangedLinesGitConfigurationsAndNames(t *testing.T) {
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
	// These repo-local settings must not alter the machine-facing diff.
	run("config", "color.ui", "always")
	run("config", "diff.noprefix", "true")
	run("config", "diff.mnemonicPrefix", "true")

	files := []string{"plain.go", "café.go", `quote".go`, `back\slash.go`, "tab\tname.go"}
	for _, name := range files {
		if err := os.WriteFile(filepath.Join(root, name), []byte("package p\n\nfunc F() {}\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	run("add", ".")
	run("commit", "-q", "-m", "base")
	for _, name := range files {
		if err := os.WriteFile(filepath.Join(root, name), []byte("package p\n\nfunc F() { println(1) }\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	m, err := ChangedLines(root, "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	if len(m) != len(files) {
		t.Fatalf("wanted %d changed paths, got %#v", len(files), m)
	}
	for _, name := range files {
		if !m[name][3] {
			t.Errorf("missed changed source line 3 in %q: %#v", name, m)
		}
	}
}

// Git ordinarily C-quotes Unicode paths when core.quotepath=true, but
// ChangedLines should work independently of that global configuration.
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
	run("config", "core.quotepath", "true")
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
