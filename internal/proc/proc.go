// Package proc runs Go toolchain subprocesses with a bounded pipe-cleanup
// period and, on supported platforms, a separately managed process group.
package proc

import (
	"context"
	"os/exec"
	"time"
)

// CleanupGrace bounds the time that Wait may spend draining pipes after a
// command exits or its context is canceled. This is cleanup time in addition
// to the caller's deadline; it is not a replacement for that deadline.
const CleanupGrace = 250 * time.Millisecond

// Command arranges for cancellation to terminate the command's descendants
// on Unix. Call Run or Output below, rather than cmd.Run/Output directly, to
// perform best-effort descendant cleanup even after a normal parent exit.
func Command(ctx context.Context, name string, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.WaitDelay = CleanupGrace
	configure(cmd)
	return cmd
}

// Run ensures descendants do not remain after the direct process exits.
func Run(cmd *exec.Cmd) error {
	err := cmd.Run()
	cleanup(cmd)
	return err
}

// Output preserves os/exec's ExitError.Stderr behavior while cleaning up
// descendants on both successful and failed commands.
func Output(cmd *exec.Cmd) ([]byte, error) {
	out, err := cmd.Output()
	cleanup(cmd)
	return out, err
}

// CombinedOutput has the same output/error semantics as exec.Cmd's method,
// including WaitDelay, and cleans up remaining descendant processes.
func CombinedOutput(cmd *exec.Cmd) ([]byte, error) {
	out, err := cmd.CombinedOutput()
	cleanup(cmd)
	return out, err
}
