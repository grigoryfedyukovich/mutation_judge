package workspace

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestR07S20CopyPlanParityAndIsolation(t *testing.T) {
	root := t.TempDir()
	mustWrite(t, filepath.Join(root, "go.mod"), "module example.test/m\n\ngo 1.22\n", 0644)
	mustWrite(t, filepath.Join(root, "p", "f.go"), "package p\nfunc F() int { return 1 }\n", 0600)
	mustWrite(t, filepath.Join(root, "p", "fixture"), "original", 0644)
	mustWrite(t, filepath.Join(root, ".mutation-judge", "cache", "skip"), "bad", 0644)
	if runtime.GOOS != "windows" {
		if err := os.Symlink("f.go", filepath.Join(root, "p", "alias")); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(filepath.Join(root, "p", "f.go"), filepath.Join(root, "p", "absolute")); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(t.TempDir(), filepath.Join(root, "p", "outbound")); err != nil {
			t.Fatal(err)
		}
	}
	plan, err := NewCopyPlan(context.Background(), root, ".mutation-judge/cache")
	if err != nil {
		t.Fatal(err)
	}
	a, ca, err := plan.Copy(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer ca()
	b, cb, err := plan.Copy(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer cb()
	baseline, cl, err := CopyModule(root, ".mutation-judge/cache")
	if err != nil {
		t.Fatal(err)
	}
	defer cl()
	for _, target := range []string{a, b, baseline} {
		content, err := os.ReadFile(filepath.Join(target, "p", "f.go"))
		if err != nil || string(content) != "package p\nfunc F() int { return 1 }\n" {
			t.Fatalf("bad copy %s: %q %v", target, content, err)
		}
		st, err := os.Stat(filepath.Join(target, "p", "f.go"))
		if err != nil || st.Mode().Perm() != 0600 {
			t.Fatalf("mode: %v %v", st, err)
		}
		if _, err := os.Stat(filepath.Join(target, ".mutation-judge")); !os.IsNotExist(err) {
			t.Fatalf("excluded directory present: %v", err)
		}
		if runtime.GOOS != "windows" {
			for _, link := range []string{"alias", "absolute"} {
				dest, err := filepath.EvalSymlinks(filepath.Join(target, "p", link))
				if err != nil || dest != filepath.Join(target, "p", "f.go") {
					t.Fatalf("bad link: %v %v", dest, err)
				}
			}
			if _, err := os.Lstat(filepath.Join(target, "p", "outbound")); !os.IsNotExist(err) {
				t.Fatalf("outbound link unexpectedly copied: %v", err)
			}
		}
	}
	// A test may change anything in its writable sandbox. Nothing escapes.
	if err := os.WriteFile(filepath.Join(a, "p", "fixture"), []byte("dirty"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(a, "p", "f.go")); err != nil {
		t.Fatal(err)
	}
	for _, target := range []string{b, baseline, root} {
		v, err := os.ReadFile(filepath.Join(target, "p", "fixture"))
		if err != nil || string(v) != "original" {
			t.Fatalf("contaminated %s: %q %v", target, v, err)
		}
		if _, err := os.Stat(filepath.Join(target, "p", "f.go")); err != nil {
			t.Fatal(err)
		}
	}
}

func TestR07S20CopyPlanCancellation(t *testing.T) {
	root := t.TempDir()
	mustWrite(t, filepath.Join(root, "p.go"), "package p", 0644)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := NewCopyPlan(ctx, root, ""); !errors.Is(err, context.Canceled) {
		t.Fatalf("NewCopyPlan err = %v", err)
	}
	plan, err := NewCopyPlan(context.Background(), root, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, cleanup, err := plan.Copy(ctx); !errors.Is(err, context.Canceled) {
		if cleanup != nil {
			cleanup()
		}
		t.Fatalf("Copy err = %v", err)
	}
}

func BenchmarkR07S20SandboxCopy(b *testing.B) {
	root := generateModule(b, 2000, 20)
	plan, err := NewCopyPlan(context.Background(), root, "")
	if err != nil {
		b.Fatal(err)
	}
	b.Run("legacy", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			_, cl, err := CopyModule(root, "")
			if err != nil {
				b.Fatal(err)
			}
			b.StopTimer()
			cl()
			b.StartTimer()
		}
	})
	b.Run("manifest", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			_, cl, err := plan.Copy(context.Background())
			if err != nil {
				b.Fatal(err)
			}
			b.StopTimer()
			cl()
			b.StartTimer()
		}
	})
}
