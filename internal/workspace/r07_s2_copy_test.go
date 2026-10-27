package workspace

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestR07S2CopyWorkersPreserveIndependentWritableTrees(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "pkg", "nested"), 0755); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 150; i++ {
		name := filepath.Join(root, "pkg", "nested", fmt.Sprintf("f%03d.go", i))
		mode := os.FileMode(0644)
		if i%11 == 0 {
			mode = 0600
		}
		if err := os.WriteFile(name, []byte(fmt.Sprintf("package p\n// f%d\n", i)), mode); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.MkdirAll(filepath.Join(root, "empty"), 0755); err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" {
		if err := os.Symlink("nested/f000.go", filepath.Join(root, "pkg", "link.go")); err != nil {
			t.Fatal(err)
		}
	}
	// Every worker count uses the same immutable source and must provide
	// independently writable copies, regardless of CoW support.
	for _, n := range []int{1, 2, 4, 8} {
		t.Run(fmt.Sprintf("workers-%d", n), func(t *testing.T) {
			t.Setenv("MUTATION_JUDGE_COPY_WORKERS", fmt.Sprint(n))
			a, cleanA, err := CopyModuleContext(context.Background(), root, "")
			if err != nil {
				t.Fatal(err)
			}
			defer cleanA()
			b, cleanB, err := CopyModuleContext(context.Background(), root, "")
			if err != nil {
				t.Fatal(err)
			}
			defer cleanB()
			for i := 0; i < 150; i++ {
				rel := filepath.Join("pkg", "nested", fmt.Sprintf("f%03d.go", i))
				for _, tree := range []string{a, b} {
					data, err := os.ReadFile(filepath.Join(tree, rel))
					if err != nil || string(data) != fmt.Sprintf("package p\n// f%d\n", i) {
						t.Fatalf("file %s copied incorrectly: %q: %v", rel, data, err)
					}
					info, err := os.Stat(filepath.Join(tree, rel))
					if err != nil {
						t.Fatal(err)
					}
					want := os.FileMode(0644)
					if i%11 == 0 {
						want = 0600
					}
					if info.Mode().Perm() != want {
						t.Fatalf("file %s mode: %o want %o", rel, info.Mode().Perm(), want)
					}
				}
			}
			rel := filepath.Join("pkg", "nested", "f000.go")
			if err := os.WriteFile(filepath.Join(a, rel), []byte("modified"), 0600); err != nil {
				t.Fatal(err)
			}
			if err := os.Remove(filepath.Join(a, "pkg", "nested", "f001.go")); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(a, "pkg", "new.txt"), []byte("new"), 0600); err != nil {
				t.Fatal(err)
			}
			for _, other := range []string{b, root} {
				data, err := os.ReadFile(filepath.Join(other, rel))
				if err != nil || string(data) != "package p\n// f0\n" {
					t.Fatalf("copy changed independent tree %s: %q %v", other, data, err)
				}
				if _, err := os.Stat(filepath.Join(other, "pkg", "nested", "f001.go")); err != nil {
					t.Fatal(err)
				}
				if _, err := os.Stat(filepath.Join(other, "pkg", "new.txt")); !os.IsNotExist(err) {
					t.Fatalf("new file leaked into %s: %v", other, err)
				}
			}
			if _, err := os.Stat(filepath.Join(a, "empty")); err != nil {
				t.Fatal(err)
			}
			if runtime.GOOS != "windows" {
				link, err := os.Readlink(filepath.Join(a, "pkg", "link.go"))
				if err != nil || link != "nested/f000.go" {
					t.Fatalf("link not preserved: %q %v", link, err)
				}
			}
		})
	}
}

func TestR07S2RejectInvalidCopyWorkerConfiguration(t *testing.T) {
	for _, raw := range []string{"0", "9", "-1", "abc", "1.5"} {
		t.Run(raw, func(t *testing.T) {
			t.Setenv("MUTATION_JUDGE_COPY_WORKERS", raw)
			root, cleanup, err := CopyModuleContext(context.Background(), t.TempDir(), "")
			if cleanup != nil {
				cleanup()
			}
			if err == nil || root != "" || !strings.Contains(err.Error(), "MUTATION_JUDGE_COPY_WORKERS") {
				t.Fatalf("unexpected result: path=%q err=%v", root, err)
			}
		})
	}
}

func TestR07S2CancelledCopyDoesNotLeaveSandbox(t *testing.T) {
	root := t.TempDir()
	for i := 0; i < 250; i++ {
		if err := os.WriteFile(filepath.Join(root, fmt.Sprintf("f%04d.txt", i)), []byte(strings.Repeat("x", 1024)), 0600); err != nil {
			t.Fatal(err)
		}
	}
	for _, n := range []string{"1", "4"} {
		t.Run(n, func(t *testing.T) {
			t.Setenv("MUTATION_JUDGE_COPY_WORKERS", n)
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			result, cleanup, err := CopyModuleContext(ctx, root, "")
			if cleanup != nil {
				cleanup()
			}
			if result != "" || !errors.Is(err, context.Canceled) {
				t.Fatalf("got path=%q err=%v", result, err)
			}
		})
	}
}

func TestR07S2ExplicitNonCloneFallback(t *testing.T) {
	root := t.TempDir()
	src := filepath.Join(root, "source")
	dst := filepath.Join(root, "dest")
	want := strings.Repeat("0123456789", 10000)
	if err := os.WriteFile(src, []byte(want), 0600); err != nil {
		t.Fatal(err)
	}
	cloned, err := copyFileWithCloneOption(context.Background(), src, dst, 0600, false)
	if cloned || err != nil {
		t.Fatalf("cloned=%v err=%v", cloned, err)
	}
	if err := os.WriteFile(dst, []byte("changed"), 0600); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(src)
	if err != nil || string(data) != want {
		t.Fatalf("fallback aliased original: %v", err)
	}
}
