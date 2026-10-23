//go:build linux || darwin || freebsd || netbsd || openbsd || dragonfly

package proc

import (
	"errors"
	"os/exec"
	"syscall"
)

func configure(cmd *exec.Cmd) {
	// This is an isolated group, not the mutation-judge process group.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return nil
		}
		err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		if errors.Is(err, syscall.ESRCH) {
			return nil
		}
		return err
	}
}

func cleanup(cmd *exec.Cmd) {
	// A direct child can exit while a grandchild continues running. The group
	// is also terminated on ordinary exit, not only on context cancellation.
	// Exits have already been waited on; the group persists if members remain.
	if cmd.Process != nil {
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
}

func runManaged(cmd *exec.Cmd) error { return cmd.Run() }
