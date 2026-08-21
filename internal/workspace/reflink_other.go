//go:build !linux

package workspace

// tryReflink is a no-op on every platform except Linux (see
// reflink_linux.go): CopyModule always falls back to its existing
// full-copy path here, which is correct on every platform, just not the
// fastest possible on ones with copy-on-write filesystem support.
//
// macOS's APFS supports an equivalent primitive (the clonefile(2)
// syscall), deliberately not implemented here. Every way to reach it
// from Go has a real downside, and this project has no way to test any
// of them -- this development environment is Linux-only, and shipping
// unverified platform-specific behavior on a platform nobody here can
// run is exactly the mistake that produced the internal/runner
// classifyEvents bug fixed earlier (see ISSUES.md); a syscall-level
// filesystem operation is a substantially higher-stakes place to repeat
// that mistake than a text classifier, since a mistake here risks
// corrupting or aliasing files in the person's real source tree instead
// of just misreporting a verdict:
//
//   - cgo, calling clonefile(2) through system headers: works, but cgo
//     is a materially bigger change than this project has made
//     elsewhere -- it affects cross-compilation and build requirements
//     project-wide, not just this one file, so it deserves its own
//     decision rather than arriving as a side effect of this feature.
//   - golang.org/x/sys/unix's Clonefileat: the safest option
//     technically, but it is a new dependency, in tension with
//     docs/decisions/0001-config-parser-scope.md's reasoning for
//     keeping this project dependency-free (that decision was about a
//     TOML/YAML library specifically, but the same reasoning about
//     auditability applies here).
//   - a raw syscall by number, as reflink_linux.go does for Linux's
//     FICLONE: Apple documents ABI stability at the libSystem.dylib
//     function level, explicitly not at the level of raw syscall
//     numbers, which have changed across macOS versions before for
//     tools that relied on them this way. Linux's FICLONE ioctl number
//     used on the Linux side of this file is a stable, long-documented
//     kernel constant; there is no equivalent stability guarantee to
//     lean on here.
//
// Revisit this once it can actually be tested on real macOS hardware,
// at which point golang.org/x/sys/unix is likely the right choice of
// the three above. See docs/performance.md for the full writeup.
func tryReflink(dst, src string) bool {
	return false
}
