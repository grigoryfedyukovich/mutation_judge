//go:build windows

package proc

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestWindowsJobHelper executes in separate test processes. The grandchild
// deliberately inherits the output handles so that its survival is visible
// as both a late marker file and an unclosed pipe.
func TestWindowsJobHelper(t *testing.T) {
	mode := os.Getenv("MJ_WIN_JOB_HELPER")
	if mode == "" {
		return
	}
	marker := os.Getenv("MJ_WIN_JOB_MARKER")
	switch mode {
	case "plain":
		fmt.Print("ready")
	case "grandchild":
		time.Sleep(1500 * time.Millisecond)
		if err := os.WriteFile(marker, []byte("escaped"), 0600); err != nil {
			panic(err)
		}
	case "spawn-wait", "spawn-exit":
		grandchild := exec.Command(os.Args[0], "-test.run=^TestWindowsJobHelper$")
		grandchild.Env = append(os.Environ(), "MJ_WIN_JOB_HELPER=grandchild", "MJ_WIN_JOB_MARKER="+marker)
		grandchild.Stdout, grandchild.Stderr = os.Stdout, os.Stderr
		if err := grandchild.Start(); err != nil {
			panic(err)
		}
		if err := os.WriteFile(marker+".ready", []byte("started"), 0600); err != nil {
			panic(err)
		}
		if mode == "spawn-exit" {
			os.Exit(0)
		}
		if err := grandchild.Wait(); err != nil {
			panic(err)
		}
	default:
		panic("unknown helper mode: " + mode)
	}
}

func windowsJobCmd(ctx context.Context, mode, marker string) *exec.Cmd {
	cmd := Command(ctx, os.Args[0], "-test.run=^TestWindowsJobHelper$")
	cmd.Env = append(os.Environ(), "MJ_WIN_JOB_HELPER="+mode, "MJ_WIN_JOB_MARKER="+marker)
	return cmd
}

func waitForWindowsMarker(t *testing.T, name string) {
	t.Helper()
	until := time.Now().Add(3 * time.Second)
	for time.Now().Before(until) {
		if _, err := os.Stat(name); err == nil {
			return
		}
		time.Sleep(15 * time.Millisecond)
	}
	t.Fatalf("helper did not create readiness marker %q", name)
}

func assertWindowsGrandchildTerminated(t *testing.T, marker string) {
	t.Helper()
	time.Sleep(1700 * time.Millisecond) // beyond the descendant's marker delay
	if _, err := os.Stat(marker); err == nil {
		t.Fatalf("descendant survived Job Object termination and wrote %s", marker)
	} else if !os.IsNotExist(err) {
		t.Fatal(err)
	}
}

func TestWindowsJobResumesChildAfterAssignment(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	out, err := Output(windowsJobCmd(ctx, "plain", ""))
	if err != nil || !strings.Contains(string(out), "ready") {
		t.Fatalf("suspended child not resumed: output %q; error %v", out, err)
	}
}

func TestWindowsJobCancellationKillsDescendants(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "escaped.txt")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cmd := windowsJobCmd(ctx, "spawn-wait", marker)
	finished := make(chan error, 1)
	go func() { finished <- Run(cmd) }()
	waitForWindowsMarker(t, marker+".ready")
	start := time.Now()
	cancel()
	select {
	case err := <-finished:
		if err == nil {
			t.Fatal("expected canceled subprocess failure")
		}
		if time.Since(start) > time.Second {
			t.Fatalf("cleanup too slow: %s", time.Since(start))
		}
	case <-time.After(3 * time.Second):
		t.Fatal("cancel did not terminate Job Object promptly")
	}
	assertWindowsGrandchildTerminated(t, marker)
}

func TestWindowsJobDeadlineKillsDescendants(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "escaped.txt")
	ctx, cancel := context.WithTimeout(context.Background(), 900*time.Millisecond)
	defer cancel()
	cmd := windowsJobCmd(ctx, "spawn-wait", marker)
	started := time.Now()
	finished := make(chan error, 1)
	go func() { _, err := CombinedOutput(cmd); finished <- err }()
	// Verify a grandchild really started before the deadline. Otherwise a
	// canceled command might pass without ever exercising job termination.
	waitForWindowsMarker(t, marker+".ready")
	select {
	case err := <-finished:
		if err == nil {
			t.Fatal("expected subprocess timeout")
		}
		if time.Since(started) > 1600*time.Millisecond {
			t.Fatalf("timeout overrun: %s; %v", time.Since(started), err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("deadline did not terminate Job Object promptly")
	}
	assertWindowsGrandchildTerminated(t, marker)
}

func TestWindowsJobExitedParentCannotLeaveDescendantsOrPipes(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "escaped.txt")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	started := time.Now()
	_, err := Output(windowsJobCmd(ctx, "spawn-exit", marker))
	if elapsed := time.Since(started); elapsed > 1200*time.Millisecond {
		t.Fatalf("descendant-held pipe exceeded cleanup grace: %s; %v", elapsed, err)
	}
	// Go's WaitDelay reports truncated pipes on this path. On Windows
	// setups where inheritance is restricted, no truncation is also valid.
	if err != nil && !errors.Is(err, exec.ErrWaitDelay) {
		t.Fatalf("unexpected wait error: %v", err)
	}
	assertWindowsGrandchildTerminated(t, marker)
}
