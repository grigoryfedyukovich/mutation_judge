// Package proc runs Go toolchain subprocesses with bounded output and a
// bounded pipe-cleanup period. Supported Unix platforms also terminate the
// entire subprocess group on cancellation.
package proc

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"sync"
	"time"
)

// CleanupGrace is added after a command's deadline for pipe teardown.
const CleanupGrace = 250 * time.Millisecond

// MaxCaptureBytes limits retained output from every preparation command.
// An oversized command is an explicit error, never a valid truncated parse.
// In particular, huge go-list JSON or Git diffs must not silently omit inputs.
const MaxCaptureBytes = 32 << 20
const maxStderrBytes = 64 << 10

// ErrOutputLimit allows callers to distinguish resource exhaustion from an
// ordinary toolchain error using errors.Is.
var ErrOutputLimit = errors.New("subprocess output exceeds capture budget")

type boundedCapture struct {
	mu       sync.Mutex
	bytes    []byte
	limit    int
	exceeded bool
}

func (w *boundedCapture) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	n := len(p)
	remaining := w.limit - len(w.bytes)
	if remaining < n {
		w.exceeded = true
		if remaining > 0 {
			w.bytes = append(w.bytes, p[:remaining]...)
		}
	} else {
		w.bytes = append(w.bytes, p...)
	}
	// Drain even after exhaustion: terminating the copy goroutine would
	// close the pipe and risk blocking or changing the child verdict.
	return n, nil
}

func captureError(stream string, limit int) error {
	return fmt.Errorf("%w (%s, limit %d bytes)", ErrOutputLimit, stream, limit)
}

// Command manages descendant processes and bounds pipe draining on wait.
// Use Run, Output, or CombinedOutput rather than cmd.Run directly.
func Command(ctx context.Context, name string, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.WaitDelay = CleanupGrace
	configure(cmd)
	return cmd
}

func Run(cmd *exec.Cmd) error {
	err := cmd.Run()
	cleanup(cmd)
	return err
}

// Output has the same stdout-on-success contract as exec.Cmd.Output and
// preserves ExitError.Stderr. Both streams have independent memory budgets.
func Output(cmd *exec.Cmd) ([]byte, error) {
	if cmd.Stdout != nil {
		return nil, errors.New("exec: Stdout already set")
	}
	stdout := &boundedCapture{limit: MaxCaptureBytes}
	cmd.Stdout = stdout
	var stderr *boundedCapture
	if cmd.Stderr == nil {
		stderr = &boundedCapture{limit: maxStderrBytes}
		cmd.Stderr = stderr
	}
	err := Run(cmd)
	if stderr != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			exitErr.Stderr = append([]byte(nil), stderr.bytes...)
		}
	}
	if stdout.exceeded {
		err = errors.Join(err, captureError("stdout", MaxCaptureBytes))
	}
	if stderr != nil && stderr.exceeded {
		err = errors.Join(err, captureError("stderr", maxStderrBytes))
	}
	return stdout.bytes, err
}

// CombinedOutput captures stdout/stderr in their arrival order, with a
// single budget across both streams. An overflow always returns an error.
func CombinedOutput(cmd *exec.Cmd) ([]byte, error) {
	if cmd.Stdout != nil || cmd.Stderr != nil {
		return nil, errors.New("exec: Stdout/Stderr already set")
	}
	combined := &boundedCapture{limit: MaxCaptureBytes}
	cmd.Stdout, cmd.Stderr = combined, combined
	err := Run(cmd)
	if combined.exceeded {
		err = errors.Join(err, captureError("combined stdout/stderr", MaxCaptureBytes))
	}
	return combined.bytes, err
}
