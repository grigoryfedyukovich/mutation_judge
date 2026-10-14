//go:build linux || darwin || freebsd || netbsd || openbsd || dragonfly

package runner

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/example/mutation-judge/internal/model"
)

func f07FakeGo(t *testing.T, shell string) (string, string) {
	t.Helper()
	dir := t.TempDir()
	marker := filepath.Join(dir, "late")
	exe := filepath.Join(dir, "go")
	script := "#!/bin/sh\n" + strings.ReplaceAll(shell, "@MARKER@", "'"+marker+"'") + "\n"
	if err := os.WriteFile(exe, []byte(script), 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return dir, marker
}

func f07NoLateChild(t *testing.T, marker string) {
	t.Helper()
	// The descendant would create this marker if it survived the group kill.
	time.Sleep(650 * time.Millisecond)
	if _, err := os.Stat(marker); err == nil {
		t.Errorf("descendant survived subprocess cleanup: %s created", marker)
	} else if !os.IsNotExist(err) {
		t.Fatal(err)
	}
}

func TestF07GoTestTimeoutKillsDescendantsAndBoundsPipes(t *testing.T) {
	// The child inherits the stdout pipe and would live past the direct
	// process's deadline without process-group cancellation.
	dir, marker := f07FakeGo(t, "(sleep 0.5; echo escaped > @MARKER@) &\nwait")
	start := time.Now()
	got := (GoTest{}).Run(context.Background(), Request{
		Root: dir, WorkRel: ".", Patterns: []string{"."}, Timeout: 70 * time.Millisecond,
	})
	if took := time.Since(start); took > 550*time.Millisecond {
		t.Fatalf("timeout took %s, want <= 550ms (including cleanup)", took)
	}
	if got.Verdict != model.VerdictTimeout {
		t.Fatalf("verdict = %s, want TIMEOUT: %s", got.Verdict, got.Output)
	}
	f07NoLateChild(t, marker)
}

func TestF07GoTestParentCancellationKillsDescendants(t *testing.T) {
	dir, marker := f07FakeGo(t, "(sleep 0.5; echo escaped > @MARKER@) &\nwait")
	parent, cancel := context.WithCancel(context.Background())
	go func() { time.Sleep(65 * time.Millisecond); cancel() }()
	start := time.Now()
	got := (GoTest{}).Run(parent, Request{Root: dir, WorkRel: ".", Patterns: []string{"."}, Timeout: 3 * time.Second})
	if took := time.Since(start); took > 550*time.Millisecond {
		t.Fatalf("parent cancellation took %s", took)
	}
	if got.Verdict != model.VerdictUnknown {
		t.Fatalf("verdict = %s, want UNKNOWN", got.Verdict)
	}
	f07NoLateChild(t, marker)
}

func TestF07ExitedParentHoldingPipesCannotBlockOrLeaveChild(t *testing.T) {
	// Parent exits successfully, but a grandchild holds both output pipes.
	// WaitDelay bounds the drain and cleanup kills the remaining group.
	dir, marker := f07FakeGo(t, "(sleep 0.5; echo escaped > @MARKER@) &\nexit 0")
	start := time.Now()
	got := (GoTest{}).Run(context.Background(), Request{Root: dir, WorkRel: ".", Patterns: []string{"."}, Timeout: 3 * time.Second})
	if took := time.Since(start); took > 550*time.Millisecond {
		t.Fatalf("pipe drain took %s", took)
	}
	if got.Verdict != model.VerdictUnknown {
		t.Fatalf("output stream truncated but verdict = %s, want UNKNOWN", got.Verdict)
	}
	f07NoLateChild(t, marker)
}

func TestF07ListingUsesSameDeadlineAndDescendantPolicy(t *testing.T) {
	dir, marker := f07FakeGo(t, "(sleep 0.5; echo escaped > @MARKER@) &\nwait")
	start := time.Now()
	_, err := ListRunnableUnitsWithEnvironment(context.Background(), dir, ".", ".", 70*time.Millisecond, nil)
	if err == nil {
		t.Fatal("expected listing deadline error")
	}
	if took := time.Since(start); took > 550*time.Millisecond {
		t.Fatalf("listing took %s", took)
	}
	f07NoLateChild(t, marker)
}

func TestF07ToolchainDetectionCanBeCanceled(t *testing.T) {
	_, marker := f07FakeGo(t, "(sleep 0.5; echo escaped > @MARKER@) &\nwait")
	ctx, cancel := context.WithTimeout(context.Background(), 70*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err := DetectToolchain(ctx)
	if err == nil {
		t.Fatal("expected cancellation")
	}
	if took := time.Since(start); took > 550*time.Millisecond {
		t.Fatalf("go env took %s", took)
	}
	f07NoLateChild(t, marker)
}
