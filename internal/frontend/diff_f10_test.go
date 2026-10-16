package frontend

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/example/mutation-judge/internal/model"
)

// TestMutationDiffGitApply independently checks the patch artifact against
// git apply and the direct mutation span; checking only source reconstruction
// would miss invalid hunk counts and EOF markers.
func TestMutationDiffGitApply(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("requires git apply")
	}
	cases := []struct {
		name, file, code, rule string
	}{
		{"last line with newline", "p.go", "package p\nfunc F(x int) bool { return x > 0 }\n", "MJ-BOUNDARY"},
		{"last line without newline", "p.go", "package p\nfunc F(x int) bool { return x > 0 }", "MJ-BOUNDARY"},
		{"middle line", "p.go", "package p\nfunc F(x int) bool { return x > 0 }\nfunc G() bool { return true }\n", "MJ-BOUNDARY"},
		{"no final newline after other lines", "p.go", "package p\nfunc F(x int) bool { return x > 0 }\nfunc G() bool { return true }", "MJ-BOUNDARY"},
		{"multiline deletion with newline", "p.go", "package p\nfunc F(a, b bool) bool {\n\treturn a &&\n\t\tb\n}\n", "MJ-BOOL-DROP-RIGHT"},
		{"multiline deletion without newline", "p.go", "package p\nfunc F(a, b bool) bool {\n\treturn a &&\n\t\tb\n}", "MJ-BOOL-DROP-RIGHT"},
		{"spaces in filename", "file name.go", "package p\nfunc F(x int) bool { return x > 0 }\n", "MJ-BOUNDARY"},
		{"quote in filename", "quote\".go", "package p\nfunc F(x int) bool { return x > 0 }\n", "MJ-BOUNDARY"},
		{"backslash in filename", "back\\slash.go", "package p\nfunc F(x int) bool { return x > 0 }\n", "MJ-BOUNDARY"},
		{"tab in filename", "tab\tfile.go", "package p\nfunc F(x int) bool { return x > 0 }\n", "MJ-BOUNDARY"},
		{"unicode in filename", "café.go", "package p\nfunc F(x int) bool { return x > 0 }\n", "MJ-BOUNDARY"},
		{"nested unusual path", "a weird/quote\".go", "package p\nfunc F(x int) bool { return x > 0 }\n", "MJ-BOUNDARY"},
		{"newline in filename", "line\nfile.go", "package p\nfunc F(x int) bool { return x > 0 }\n", "MJ-BOUNDARY"},
		{"control character in filename", "control\x01.go", "package p\nfunc F(x int) bool { return x > 0 }\n", "MJ-BOUNDARY"},
		{"CRLF source", "crlf.go", "package p\r\nfunc F(x int) bool { return x > 0 }\r\n", "MJ-BOUNDARY"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			path := filepath.Join(root, tc.file)
			if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, []byte(tc.code), 0644); err != nil {
				t.Fatal(err)
			}
			ops := map[string]bool{"boundary": true}
			if tc.rule == "MJ-BOOL-DROP-RIGHT" {
				ops = map[string]bool{"boolean": true}
			}
			mutants, err := Discover(root, []string{tc.file}, Options{Operators: ops})
			if err != nil {
				t.Fatal(err)
			}
			var selected *model.Mutation
			for i := range mutants {
				if tc.rule == "MJ-BOUNDARY" && mutants[i].Operator == "boundary" || mutants[i].RuleID == tc.rule {
					selected = &mutants[i]
					break
				}
			}
			if selected == nil {
				t.Fatalf("missing mutation for %s; got %d candidates", tc.rule, len(mutants))
			}
			m := *selected
			before := []byte(tc.code)
			if got := string(before[m.Span.StartByte:m.Span.EndByte]); got != m.Original {
				t.Fatalf("unexpected original %q, expected %q", got, m.Original)
			}
			want := append([]byte{}, before[:m.Span.StartByte]...)
			want = append(want, m.Replacement...)
			want = append(want, before[m.Span.EndByte:]...)
			for _, args := range [][]string{{"apply", "--check", "-"}, {"apply", "-"}} {
				cmd := exec.Command("git", args...)
				cmd.Dir = root
				cmd.Stdin = strings.NewReader(m.Diff)
				out, err := cmd.CombinedOutput()
				if err != nil {
					t.Fatalf("git %v failed: %v\n%s\npatch:\n%s", args, err, out, m.Diff)
				}
			}
			got, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(got, want) {
				t.Fatalf("patch result mismatch\ngot:  %q\nwant: %q\npatch:\n%s", got, want, m.Diff)
			}
			if tc.name == "last line without newline" && !strings.Contains(m.Diff, "\\ No newline at end of file") {
				t.Fatalf("patch lacks EOF marker:\n%s", m.Diff)
			}
			if tc.name == "last line with newline" && strings.Contains(m.Diff, "\\ No newline at end of file") {
				t.Fatalf("patch contains unexpected EOF marker:\n%s", m.Diff)
			}
		})
	}
}

func TestDiffLinesDoNotInventPhantomNewline(t *testing.T) {
	for _, tc := range []struct {
		block string
		want  int
	}{
		{"", 0}, {"first\n", 1}, {"first\nsecond\n", 2}, {"first\nsecond", 2}, {"\n", 1}, {"first\n\n", 2},
	} {
		if got := len(diffLines([]byte(tc.block))); got != tc.want {
			t.Error(fmt.Sprintf("diffLines(%q): got %d lines; want %d", tc.block, got, tc.want))
		}
	}
}

func TestEveryDiscoveredMutationPatchReplays(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("requires git apply")
	}
	const source = `package p

func F(n int, x, y bool) bool {
	if n > 0 && x {
		return x && y
	}
	if n == 0 {
		return !x
	}
	return x || y
}

func G(a, b int) int {
	x := a + b
	if x >= 3 { return x - 1 }
	return 7
}
`
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "p.go"), []byte(source), 0644); err != nil {
		t.Fatal(err)
	}
	mutants, err := Discover(root, []string{"p.go"}, Options{Operators: map[string]bool{
		"boundary": true, "boolean": true, "arithmetic": true, "relational": true,
		"literal": true, "condition": true, "statement": true, "assignment": true,
	}})
	if err != nil {
		t.Fatal(err)
	}
	if len(mutants) < 10 {
		t.Fatalf("expected diverse mutations, got %d", len(mutants))
	}
	for _, m := range mutants {
		if err := os.WriteFile(filepath.Join(root, "p.go"), []byte(source), 0644); err != nil {
			t.Fatal(err)
		}
		cmd := exec.Command("git", "apply", "-")
		cmd.Dir = root
		cmd.Stdin = strings.NewReader(m.Diff)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Errorf("rule %s (%s): git apply: %v\n%s\n%s", m.RuleID, m.ID, err, out, m.Diff)
			continue
		}
		got, err := os.ReadFile(filepath.Join(root, "p.go"))
		if err != nil {
			t.Fatal(err)
		}
		want := source[:m.Span.StartByte] + m.Replacement + source[m.Span.EndByte:]
		if string(got) != want {
			t.Errorf("rule %s (%s): applied patch differs from mutation replacement", m.RuleID, m.ID)
		}
	}
}
