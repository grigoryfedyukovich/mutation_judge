package workspace

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestR03VerifyModuleCopyDetectsHostEdits(t *testing.T) {
	for _, tc := range []struct {
		name  string
		alter func(string) error
	}{
		{"changed", func(root string) error {
			return os.WriteFile(filepath.Join(root, "a.go"), []byte("package p\nvar A=2\n"), 0644)
		}},
		{"added", func(root string) error {
			return os.WriteFile(filepath.Join(root, "added.go"), []byte("package p\n"), 0644)
		}},
		{"removed", func(root string) error { return os.Remove(filepath.Join(root, "a.go")) }},
		{"mode", func(root string) error { return os.Chmod(filepath.Join(root, "a.go"), 0755) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			if err := os.WriteFile(filepath.Join(root, "a.go"), []byte("package p\nvar A=1\n"), 0644); err != nil {
				t.Fatal(err)
			}
			snap, clean, err := CopyModule(root, ".cache")
			if err != nil {
				t.Fatal(err)
			}
			defer clean()
			if err := VerifyModuleCopy(root, snap, ".cache"); err != nil {
				t.Fatalf("untouched snapshot rejected: %v", err)
			}
			if err := tc.alter(root); err != nil {
				t.Fatal(err)
			}
			if err := VerifyModuleCopy(root, snap, ".cache"); err == nil || !strings.Contains(err.Error(), "snapshot acquisition") {
				t.Fatalf("missed mutation, got %v", err)
			}
		})
	}
}
