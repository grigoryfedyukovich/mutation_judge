package workspace

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"testing"
	"time"
)

func TestSymlinkDotDotThroughSymlinkIsNotCopied(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink permissions vary on Windows")
	}
	root := t.TempDir()
	name := "mj-external-" + strconv.FormatInt(time.Now().UnixNano(), 36)
	marker := filepath.Join(filepath.Dir(root), name)
	if err := os.WriteFile(marker, []byte("external-value"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Remove(marker) })
	if err := os.Symlink(".", filepath.Join(root, "d")); err != nil {
		t.Fatal(err)
	}
	linkTarget := "d/../" + name
	if err := os.Symlink(linkTarget, filepath.Join(root, "link")); err != nil {
		t.Fatal(err)
	}
	if symlinkTargetInsideRoot(mustAbs(t, root), filepath.Join(root, "link"), linkTarget) {
		t.Fatal("lexical containment admitted a symlink escape")
	}
	for _, copyFn := range []func() (string, func(), error){
		func() (string, func(), error) { return CopyModule(root, ".mutation-judge/cache") },
		func() (string, func(), error) {
			plan, err := NewCopyPlan(context.Background(), root, ".mutation-judge/cache")
			if err != nil {
				return "", nil, err
			}
			return plan.Copy(context.Background())
		},
	} {
		snap, cleanup, err := copyFn()
		if err != nil {
			t.Fatal(err)
		}
		if _, err := os.Lstat(filepath.Join(snap, "link")); !os.IsNotExist(err) {
			cleanup()
			t.Fatalf("escape link was copied into %s (%v)", snap, err)
		}
		cleanup()
	}
}

func TestNestedSymlinkBeforeDotDotIsNotCopied(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink permissions vary on Windows")
	}
	root := t.TempDir()
	name := "mj-nested-" + strconv.FormatInt(time.Now().UnixNano(), 36)
	marker := filepath.Join(filepath.Dir(root), name)
	if err := os.WriteFile(marker, []byte("external-value"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Remove(marker) })
	if err := os.Mkdir(filepath.Join(root, "mid"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(".", filepath.Join(root, "mid", "d")); err != nil {
		t.Fatal(err)
	}
	linkTarget := "mid/d/../../" + name
	if err := os.Symlink(linkTarget, filepath.Join(root, "link")); err != nil {
		t.Fatal(err)
	}
	snap, cleanup, err := CopyModule(root, ".mutation-judge/cache")
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	if _, err := os.Lstat(filepath.Join(snap, "link")); !os.IsNotExist(err) {
		t.Fatalf("nested escape link was copied (%v)", err)
	}
}

func TestCyclicSymlinkOmittedAndInternalLinksKept(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink permissions vary on Windows")
	}
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "file.txt"), []byte("inside"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(".", filepath.Join(root, "d")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("d/file.txt", filepath.Join(root, "through")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("missing.txt", filepath.Join(root, "dangling")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("cycle", filepath.Join(root, "cycle")); err != nil {
		t.Fatal(err)
	}
	snap, cleanup, err := CopyModule(root, ".mutation-judge/cache")
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	got, err := os.ReadFile(filepath.Join(snap, "through"))
	if err != nil || string(got) != "inside" {
		t.Fatalf("internal link through a directory symlink = %q, %v", got, err)
	}
	if _, err := os.Lstat(filepath.Join(snap, "dangling")); err != nil {
		t.Fatalf("in-tree dangling symlink should be kept: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(snap, "cycle")); !os.IsNotExist(err) {
		t.Fatalf("cyclic symlink should be omitted, err=%v", err)
	}
}

func TestRootBinKeepsNestedGoPackage(t *testing.T) {
	root := t.TempDir()
	mustWrite(t, filepath.Join(root, "doc.go"), "package pruned\n", 0o644)
	mustWrite(t, filepath.Join(root, "bin", "tool", "tool.go"), "package tool\nfunc Positive(x int) bool { return x > 0 }\n", 0o644)
	mustWrite(t, filepath.Join(root, "bin", "onlybin"), "not a go file\n", 0o644)
	snap, cleanup, err := CopyModule(root, ".mutation-judge/cache")
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	if _, err := os.Stat(filepath.Join(snap, "bin", "tool", "tool.go")); err != nil {
		t.Fatalf("nested package under bin/ was omitted: %v", err)
	}
	binOnly := t.TempDir()
	mustWrite(t, filepath.Join(binOnly, "doc.go"), "package pruned\n", 0o644)
	mustWrite(t, filepath.Join(binOnly, "bin", "tool"), "#!/bin/sh\n", 0o755)
	snap2, cleanup2, err := CopyModule(binOnly, ".mutation-judge/cache")
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup2()
	if _, err := os.Stat(filepath.Join(snap2, "bin")); !os.IsNotExist(err) {
		t.Fatalf("binaries-only root bin/ should still be omitted, err=%v", err)
	}
}

func mustAbs(t *testing.T, path string) string {
	t.Helper()
	abs, err := filepath.Abs(path)
	if err != nil {
		t.Fatal(err)
	}
	return abs
}
