package workspace

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"testing"
)

// oldDigestReference intentionally retains the pre-R07-S1 os.ReadFile path.
// This is an independent compatibility oracle for the byte stream hashed by
// Digest, including path/kind/mode framing, directory entries, and symlinks.
func oldDigestReference(root, cacheDir string) (string, error) {
	type entry struct {
		rel, kind, target string
		mode              os.FileMode
	}
	abs, err := filepath.Abs(root)
	if err != nil {
		return "", err
	}
	var entries []entry
	err = sandboxEntries(root, cacheDir, func(path, rel string, d fs.DirEntry) error {
		if d.IsDir() {
			entries = append(entries, entry{rel: rel, kind: "directory"})
			return nil
		}
		if d.Type()&os.ModeSymlink != 0 {
			target, err := os.Readlink(path)
			if err != nil {
				return err
			}
			entries = append(entries, entry{rel: rel, kind: "symlink", target: fingerprintSymlinkTarget(abs, target)})
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("unsupported digest entry: %s", rel)
		}
		entries = append(entries, entry{rel: rel, kind: "file", mode: info.Mode().Perm()})
		return nil
	})
	if err != nil {
		return "", err
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].rel < entries[j].rel })
	h := sha256.New()
	for _, e := range entries {
		_, _ = io.WriteString(h, e.rel)
		_, _ = h.Write([]byte{0})
		_, _ = io.WriteString(h, e.kind)
		_, _ = h.Write([]byte{0})
		switch e.kind {
		case "symlink":
			_, _ = io.WriteString(h, e.target)
		case "file":
			_, _ = io.WriteString(h, fmt.Sprintf("%04o", e.mode))
			_, _ = h.Write([]byte{0})
			b, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(e.rel)))
			if err != nil {
				return "", err
			}
			_, _ = h.Write(b)
		}
		_, _ = h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func TestR07S1DigestMatchesPreviousHashAcrossChunkBoundaries(t *testing.T) {
	root := t.TempDir()
	mustWrite(t, filepath.Join(root, "go.mod"), "module example.test/stream\n\ngo 1.22\n", 0o644)
	for _, n := range []int{0, 1, 32767, 32768, 32769, 131073} {
		data := make([]byte, n)
		for i := range data {
			data[i] = byte(i*31 + 7)
		}
		mustWrite(t, filepath.Join(root, fmt.Sprintf("fixtures/%06d.bin", n)), string(data), 0o644)
	}
	mustWrite(t, filepath.Join(root, "bin/exec.sh"), "#!/bin/sh\n", 0o755)
	mustWrite(t, filepath.Join(root, ".git", "ignored"), "not part of digest", 0o644)
	mustWrite(t, filepath.Join(root, "cache", "ignored"), "not part of digest", 0o644)
	if runtime.GOOS != "windows" {
		if err := os.Symlink("../go.mod", filepath.Join(root, "fixtures", "linked")); err != nil {
			t.Fatal(err)
		}
	}
	got, err := Digest(root, "cache")
	if err != nil {
		t.Fatal(err)
	}
	want, err := oldDigestReference(root, "cache")
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("streamed digest differs from historical digest: %s vs %s", got, want)
	}
	path := filepath.Join(root, "fixtures", "032769.bin")
	f, err := os.OpenFile(path, os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteAt([]byte{0xff}, 32768); err != nil {
		f.Close()
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	changed, err := Digest(root, "cache")
	if err != nil {
		t.Fatal(err)
	}
	if changed == got {
		t.Fatal("changing bytes after a chunk boundary did not invalidate the digest")
	}
	want, err = oldDigestReference(root, "cache")
	if err != nil || changed != want {
		t.Fatalf("changed digest is incompatible with historical implementation: %s vs %s (%v)", changed, want, err)
	}
}

// R07-S1 memory regression: the previous os.ReadFile allocated 64 MiB for
// this fixture. TotalAlloc bounds total heap allocations for this call,
// rather than relying on GC timing or platform-dependent resident memory.
func TestR07S1DigestLargeFileUsesBoundedAllocations(t *testing.T) {
	if testing.Short() {
		t.Skip("64 MiB large-file allocation regression")
	}
	root := t.TempDir()
	mustWrite(t, filepath.Join(root, "go.mod"), "module example.test/stream\n", 0o644)
	path := filepath.Join(root, "testdata", "large.bin")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Truncate(64 << 20); err != nil {
		f.Close()
		t.Skipf("cannot create large fixture on this filesystem: %v", err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	runtime.GC()
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	if _, err := Digest(root, ".mutation-judge/cache"); err != nil {
		t.Fatal(err)
	}
	runtime.ReadMemStats(&after)
	const maxAllocated = 8 << 20
	delta := after.TotalAlloc - before.TotalAlloc
	t.Logf("64 MiB file: Digest allocated %.3f MiB", float64(delta)/(1<<20))
	if delta > maxAllocated {
		t.Fatalf("Digest allocated %d bytes to hash a 64 MiB file (max %d): possible whole-file buffering", delta, maxAllocated)
	}
}
