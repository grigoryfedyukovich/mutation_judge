package runner

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/example/mutation-judge/internal/model"
)

func TestFailingTests(t *testing.T) {
	got := failingTests("--- FAIL: TestB (0.00s)\n--- FAIL: TestA (0.00s)\n")
	if len(got) != 2 || got[0] != "TestA" || got[1] != "TestB" {
		t.Fatalf("%v", got)
	}
}

func TestTrimOutputPreservesUTF8(t *testing.T) {
	const max = 8
	input := strings.Repeat("a", max-1) + "é" + "tail"
	got := trimOutput(input, max)
	if !utf8.ValidString(got) {
		t.Fatalf("trimmed output is invalid UTF-8: %q", got)
	}
	if !strings.Contains(got, "output truncated") {
		t.Fatalf("missing truncation marker: %q", got)
	}
}

func TestGoTestClassifiesCompileFailureAsInvalid(t *testing.T) {
	root := testModule(t, "package p\nfunc F() int { return missing }\n", "")
	got := (GoTest{}).Run(context.Background(), Request{Root: root, WorkRel: ".", Patterns: []string{"."}, Timeout: 2 * time.Second})
	if got.Verdict != model.VerdictInvalid {
		t.Fatalf("verdict=%s output=%s", got.Verdict, got.Output)
	}
}

func TestGoTestClassifiesRuntimePanicAsKilled(t *testing.T) {
	root := testModule(t, "package p\nfunc F() {}\n", "package p\nimport \"testing\"\nfunc TestPanic(t *testing.T) { panic(\"boom\") }\n")
	got := (GoTest{}).Run(context.Background(), Request{Root: root, WorkRel: ".", Patterns: []string{"."}, Timeout: 2 * time.Second})
	if got.Verdict != model.VerdictKilled {
		t.Fatalf("verdict=%s output=%s", got.Verdict, got.Output)
	}
}

func TestGoTestClassifiesDeadlineAsTimeout(t *testing.T) {
	root := testModule(t, "package p\nfunc F() {}\n", "package p\nimport (\"testing\"; \"time\")\nfunc TestSlow(t *testing.T) { time.Sleep(5*time.Second) }\n")
	got := (GoTest{}).Run(context.Background(), Request{Root: root, WorkRel: ".", Patterns: []string{"."}, Timeout: 50 * time.Millisecond})
	if got.Verdict != model.VerdictTimeout {
		t.Fatalf("verdict=%s output=%s", got.Verdict, got.Output)
	}
}

func TestGoTestStartFailureIsUnknown(t *testing.T) {
	t.Setenv("PATH", "")
	got := (GoTest{}).Run(context.Background(), Request{Root: t.TempDir(), WorkRel: ".", Patterns: []string{"."}, Timeout: time.Second})
	if got.Verdict != model.VerdictUnknown {
		t.Fatalf("verdict=%s output=%s", got.Verdict, got.Output)
	}
}

func testModule(t *testing.T, source, testSource string) string {
	t.Helper()
	root := t.TempDir()
	files := map[string]string{
		"go.mod": "module example.test/runnerfixture\n\ngo 1.22\n",
		"p.go":   source,
	}
	if testSource != "" {
		files["p_test.go"] = testSource
	}
	for name, data := range files {
		if err := os.WriteFile(filepath.Join(root, name), []byte(data), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root
}
