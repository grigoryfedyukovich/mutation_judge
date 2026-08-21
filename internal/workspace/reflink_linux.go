//go:build linux

package workspace

import (
	"os"
	"syscall"
)

// ficloneIoctl is the Linux FICLONE ioctl request number, defined in the
// kernel's linux/fs.h as:
//
//	#define FICLONE _IOW(0x94, 9, int)
//
// which expands to this fixed value. It is a long-stable, documented
// kernel ABI constant (present since Linux 4.5), not something obtained
// from a dependency -- defining it as a local constant here keeps this
// dependency-free, consistent with docs/decisions/0001-config-parser-scope.md's
// reasoning for the rest of this project.
const ficloneIoctl = 0x40049409

// tryReflink attempts a copy-on-write clone of src to dst via FICLONE,
// which asks the filesystem to make dst share src's data extents
// without copying them, if the underlying filesystem supports it
// (btrfs, xfs with reflink=1, ocfs2 -- not ext4, and not across
// filesystems/devices). It returns true only on a fully successful
// clone.
//
// This is always safe to call speculatively: any failure (unsupported
// filesystem, cross-device, permission, or anything else) is handled by
// removing whatever this function created and returning false, so the
// caller's existing full-copy fallback runs unchanged. A successful
// clone is not a shortcut that skips correctness guarantees either: COW
// means dst and src share data only until either is written to, at
// which point the filesystem transparently allocates a new block for
// just that write -- so a later mutation applied to dst (the sandbox
// copy) via workspace.Apply can never retroactively affect src (the
// person's real source tree). See reflink_linux_test.go, which verifies
// this isolation property directly whenever it runs on a filesystem
// that actually supports reflink.
func tryReflink(dst, src string) bool {
	srcF, err := os.Open(src)
	if err != nil {
		return false
	}
	defer srcF.Close()

	dstF, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_EXCL, 0o600)
	if err != nil {
		return false
	}

	_, _, errno := syscall.Syscall(syscall.SYS_IOCTL, dstF.Fd(), ficloneIoctl, srcF.Fd())
	closeErr := dstF.Close()
	if errno != 0 || closeErr != nil {
		_ = os.Remove(dst)
		return false
	}
	return true
}
