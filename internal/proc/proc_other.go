//go:build !linux && !darwin && !freebsd && !netbsd && !openbsd && !dragonfly

package proc

import "os/exec"

// Other platforms retain CommandContext's direct-child termination and
// bounded pipe teardown; a process-tree abstraction is not provided here.
func configure(cmd *exec.Cmd) {}
func cleanup(cmd *exec.Cmd)   {}
