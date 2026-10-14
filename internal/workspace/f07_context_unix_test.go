//go:build linux || darwin || freebsd || netbsd || openbsd || dragonfly

package workspace

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestF07PreparationGoListHonorsCancellation(t *testing.T) {
	dir := t.TempDir()
	marker := filepath.Join(dir, "escaped")
	exe := filepath.Join(dir, "go")
	script := "#!/bin/sh\n(sleep 0.5; echo late > '" + marker + "') &\nwait\n"
	if err := os.WriteFile(exe, []byte(script), 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	ctx, cancel := context.WithTimeout(context.Background(), 75*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err := ListPackagesContext(ctx, dir, []string{"./..."})
	if err == nil {
		t.Fatal("expected canceled go list")
	}
	if elapsed := time.Since(start); elapsed > 550*time.Millisecond {
		t.Fatalf("go list took %s", elapsed)
	}
	time.Sleep(650 * time.Millisecond)
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("go list descendant survived cancellation")
	} else if !os.IsNotExist(err) {
		t.Fatal(err)
	}
}
