package workspace

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestR01ReplacementSnapshotRejectsUnsafeInputs(t *testing.T) {
	t.Run("ancestor", func(t *testing.T) {
		parent := t.TempDir()
		root := filepath.Join(parent, "main")
		mustWrite(t, filepath.Join(root, "go.mod"), "module example.test/main\n\ngo 1.22\nreplace example.test/root => ..\n", 0644)
		snapshot, cleanup, err := CopyModule(root, ".mutation-judge/cache")
		if err != nil {
			t.Fatal(err)
		}
		defer cleanup()
		_, err = IsolateLocalReplacements(context.Background(), root, snapshot, ".mutation-judge/cache")
		if err == nil || !strings.Contains(err.Error(), "ancestor") {
			t.Fatalf("expected explicit ancestor rejection, got %v", err)
		}
	})
	t.Run("missing-directory", func(t *testing.T) {
		root := t.TempDir()
		mustWrite(t, filepath.Join(root, "go.mod"), "module example.test/main\n\ngo 1.22\nreplace example.test/missing => ../does-not-exist\n", 0644)
		snapshot, cleanup, err := CopyModule(root, ".mutation-judge/cache")
		if err != nil {
			t.Fatal(err)
		}
		defer cleanup()
		_, err = IsolateLocalReplacements(context.Background(), root, snapshot, ".mutation-judge/cache")
		if err == nil {
			t.Fatal("missing replacement must not run with unisolated host path")
		}
	})
	t.Run("reserved-dir-collision", func(t *testing.T) {
		parent := t.TempDir()
		root := filepath.Join(parent, "main")
		dep := filepath.Join(parent, "dep")
		mustWrite(t, filepath.Join(root, "go.mod"), "module example.test/main\n\ngo 1.22\nreplace example.test/dep => ../dep\n", 0644)
		mustWrite(t, filepath.Join(dep, "go.mod"), "module example.test/dep\n\ngo 1.22\n", 0644)
		mustWrite(t, filepath.Join(root, localReplacementsDir, "already.txt"), "user files", 0644)
		snapshot, cleanup, err := CopyModule(root, ".mutation-judge/cache")
		if err != nil {
			t.Fatal(err)
		}
		defer cleanup()
		_, err = IsolateLocalReplacements(context.Background(), root, snapshot, ".mutation-judge/cache")
		if err == nil || !strings.Contains(err.Error(), "reserved directory") {
			t.Fatalf("expected collision rejection, got %v", err)
		}
	})
	t.Run("no-dependency-does-not-care-about-reserved-name", func(t *testing.T) {
		root := t.TempDir()
		mustWrite(t, filepath.Join(root, "go.mod"), "module example.test/main\n\ngo 1.22\n", 0644)
		mustWrite(t, filepath.Join(root, localReplacementsDir, "some.txt"), "user files", 0644)
		snapshot, cleanup, err := CopyModule(root, ".mutation-judge/cache")
		if err != nil {
			t.Fatal(err)
		}
		defer cleanup()
		found, err := IsolateLocalReplacements(context.Background(), root, snapshot, ".mutation-judge/cache")
		if err != nil || found {
			t.Fatalf("no replacements should be valid: %t %v", found, err)
		}
		if _, err := os.Stat(filepath.Join(snapshot, localReplacementsDir, "some.txt")); err != nil {
			t.Fatal(err)
		}
	})
}
