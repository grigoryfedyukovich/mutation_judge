package workspace

import (
	"bytes"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

// VerifyModuleCopy detects files added, removed or altered during CopyModule.
// Subsequent host edits do not influence an already verified snapshot.
// It deliberately compares the copied bytes rather than metadata timestamps.
func VerifyModuleCopy(host, snapshot, cacheDir string) error {
	hostAbs, err := filepath.Abs(host)
	if err != nil {
		return err
	}
	seen := map[string]bool{}
	err = sandboxEntries(host, cacheDir, func(path, rel string, d fs.DirEntry) error {
		seen[rel] = true
		dst := filepath.Join(snapshot, filepath.FromSlash(rel))
		got, err := os.Lstat(dst)
		if err != nil {
			return fmt.Errorf("source changed during snapshot acquisition (%s): %w", rel, err)
		}
		if d.IsDir() {
			if !got.IsDir() {
				return fmt.Errorf("source type changed during snapshot acquisition: %s", rel)
			}
			return nil
		}
		if d.Type()&os.ModeSymlink != 0 {
			if got.Mode()&os.ModeSymlink == 0 {
				return fmt.Errorf("source type changed during snapshot acquisition: %s", rel)
			}
			target, err := os.Readlink(path)
			if err != nil {
				return err
			}
			expected, err := sandboxSymlinkTarget(hostAbs, snapshot, dst, target)
			if err != nil {
				return err
			}
			actual, err := os.Readlink(dst)
			if err != nil {
				return err
			}
			if actual != expected {
				return fmt.Errorf("symlink changed during snapshot acquisition: %s", rel)
			}
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		if !got.Mode().IsRegular() || got.Mode().Perm() != info.Mode().Perm() {
			return fmt.Errorf("source mode/type changed during snapshot acquisition: %s", rel)
		}
		a, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		b, err := os.ReadFile(dst)
		if err != nil {
			return err
		}
		if !bytes.Equal(a, b) {
			return fmt.Errorf("source bytes changed during snapshot acquisition: %s", rel)
		}
		return nil
	})
	if err != nil {
		return err
	}
	// Detect entries removed from host after they were captured. Do not follow
	// links: the snapshot is a materialized tree, not an overlay of the host.
	return sandboxEntries(snapshot, cacheDir, func(_ string, rel string, _ fs.DirEntry) error {
		if !seen[rel] {
			return fmt.Errorf("source disappeared during snapshot acquisition: %s", rel)
		}
		return nil
	})
}
