package gitdiff

import "testing"

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
