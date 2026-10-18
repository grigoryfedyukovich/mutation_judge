package runner

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/example/mutation-judge/internal/model"
)

// Race instrumentation requires atomic counters. Also verify that flags
// provided through GOFLAGS (including an incompatible default covermode)
// do not override the coverage mode selected for a profiling command.
func TestF13CoverageWithRaceEnabledGOFLAGS(t *testing.T) {
	if out, err := exec.Command("go", "env", "CGO_ENABLED").Output(); err != nil || strings.TrimSpace(string(out)) != "1" {
		t.Skip("race detector requires a cgo-enabled Go toolchain")
	}
	root := testModule(t, "package p\nfunc F(x int) bool { return x > 0 }\n",
		"package p\nimport \"testing\"\nfunc TestF(t *testing.T) { if !F(1) { t.Fatal(\"positive\") } }\n")
	for _, flags := range []string{"-race", "-race -covermode=count"} {
		t.Run(flags, func(t *testing.T) {
			t.Setenv("GOFLAGS", flags)
			profile := filepath.Join(t.TempDir(), "coverage.out")
			res := (GoTest{}).Run(context.Background(), Request{
				Root: root, WorkRel: ".", Patterns: []string{"."},
				Timeout: 120 * time.Second, CoverageOut: profile,
			})
			if res.Verdict != model.VerdictSurvived {
				t.Fatalf("GOFLAGS=%q: coverage run = %s (want SURVIVED): %s", flags, res.Verdict, res.Output)
			}
			data, err := os.ReadFile(profile)
			if err != nil {
				t.Fatalf("read profile: %v", err)
			}
			if !strings.HasPrefix(string(data), "mode: atomic\n") {
				t.Fatalf("GOFLAGS=%q: profile starts with %q (want atomic)", flags, strings.SplitN(string(data), "\n", 2)[0])
			}
		})
	}
}

func TestF13CoverageFlagInvocationErrorIsInvalid(t *testing.T) {
	// Go's pre-execution diagnostic is never evidence that a mutant was
	// killed by a test. Preserve this distinction if a future Go flag
	// combination produces a similar setup failure.
	if !fallbackBuildFailureRE.MatchString("go: -covermode must be \"atomic\", not \"count\", when -race is enabled\n") {
		t.Fatal("go coverage-mode setup failure must be classified as INVALID")
	}
}
