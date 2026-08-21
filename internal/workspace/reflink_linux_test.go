//go:build linux

package workspace

import (
	"os"
	"path/filepath"
	"testing"
)

// TestTryReflinkClonesOrCleanlyDeclines exercises tryReflink directly.
// This sandbox's own filesystem is ext4, which does not support FICLONE,
// so on it this test can only confirm the clean, no-partial-file-left-
// behind decline path -- the same path every non-Linux platform always
// takes (see reflink_other.go). On a filesystem that does support
// reflink (btrfs, xfs with reflink=1, ocfs2), this same test additionally
// verifies the property that matters most: after a successful clone,
// writing to dst must never be observable in src. That is what makes it
// safe for workspace.Apply to mutate a reflinked sandbox file without
// any risk to the person's real source tree.
func TestTryReflinkClonesOrCleanlyDeclines(t *testing.T) {
	root := t.TempDir()
	src := filepath.Join(root, "src.txt")
	dst := filepath.Join(root, "dst.txt")
	want := "original content\n"
	if err := os.WriteFile(src, []byte(want), 0o644); err != nil {
		t.Fatal(err)
	}

	ok := tryReflink(dst, src)
	if !ok {
		if _, err := os.Stat(dst); !os.IsNotExist(err) {
			t.Fatalf("tryReflink returned false but left dst behind (err=%v); the fallback copy path would fail with EEXIST-adjacent surprises", err)
		}
		t.Skip("FICLONE not supported on this filesystem (expected on ext4, which this sandbox uses); tryReflink correctly declined rather than leaving a partial file")
	}

	got, err := os.ReadFile(dst)
	if err != nil || string(got) != want {
		t.Fatalf("cloned content = %q, err=%v, want %q", got, err, want)
	}

	// The critical isolation property: a write to the clone must not
	// reach the original, even though they shared data extents a moment
	// ago.
	if err := os.WriteFile(dst, []byte("mutated\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	original, err := os.ReadFile(src)
	if err != nil || string(original) != want {
		t.Fatalf("writing to the clone changed the original: got %q, err=%v, want unchanged %q", original, err, want)
	}
}

// TestTryReflinkDoesNotCloneAcrossMissingSource proves failure paths
// (here: a nonexistent source) are handled the same clean way -- no
// partial dst left behind -- without depending on this filesystem
// actually supporting FICLONE at all, so it is not skipped here.
func TestTryReflinkDoesNotCloneAcrossMissingSource(t *testing.T) {
	root := t.TempDir()
	dst := filepath.Join(root, "dst.txt")
	if tryReflink(dst, filepath.Join(root, "does-not-exist.txt")) {
		t.Fatal("tryReflink reported success cloning a nonexistent source")
	}
	if _, err := os.Stat(dst); !os.IsNotExist(err) {
		t.Fatalf("tryReflink left dst behind after failing, err=%v", err)
	}
}
