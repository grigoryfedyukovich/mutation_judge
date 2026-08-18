package workspace

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestApplyAndRestoreAtomically(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "p.go")
	original := []byte("package p\nvar N = 1\n")
	if err := os.WriteFile(path, original, 0o600); err != nil {
		t.Fatal(err)
	}
	restore, err := Apply(root, "p.go", 18, 19, "2")
	if err != nil {
		t.Fatal(err)
	}
	mutated, _ := os.ReadFile(path)
	if string(mutated) != "package p\nvar N = 2\n" {
		t.Fatalf("unexpected mutation: %q", mutated)
	}
	if err := restore(); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(path)
	if string(got) != string(original) {
		t.Fatalf("restore mismatch: %q", got)
	}
	info, _ := os.Stat(path)
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("mode changed: %v", info.Mode().Perm())
	}
}

func TestAtomicWriteFailureLeavesOriginalUntouched(t *testing.T) {
	path := filepath.Join(t.TempDir(), "p.go")
	original := []byte("original")
	if err := os.WriteFile(path, original, 0o644); err != nil {
		t.Fatal(err)
	}
	wantErr := errors.New("rename failed")
	err := writeFileAtomicWithRename(path, []byte("mutated"), 0o644, func(_, _ string) error { return wantErr })
	if !errors.Is(err, wantErr) {
		t.Fatalf("unexpected error: %v", err)
	}
	got, _ := os.ReadFile(path)
	if string(got) != string(original) {
		t.Fatalf("failed atomic write changed destination: %q", got)
	}
}

func TestApplyRejectsInvalidAndEscapingSpans(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "p.go"), []byte("package p\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name       string
		rel        string
		start, end int
	}{
		{"escape", "../outside.go", 0, 1},
		{"zero", "p.go", 2, 2},
		{"reversed", "p.go", 4, 2},
		{"past-end", "p.go", 0, 99},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := Apply(root, tc.rel, tc.start, tc.end, "x"); err == nil {
				t.Fatal("expected rejection")
			}
		})
	}
}

func TestApplyRejectsSymlinkOutsideSandbox(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink permissions vary on Windows")
	}
	outside := filepath.Join(t.TempDir(), "outside.go")
	if err := os.WriteFile(outside, []byte("package p\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(root, "p.go")); err != nil {
		t.Fatal(err)
	}
	if _, err := Apply(root, "p.go", 0, 1, "x"); err == nil {
		t.Fatal("expected symlink escape rejection")
	}
}
