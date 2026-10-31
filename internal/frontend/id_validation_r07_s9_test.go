package frontend

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func assertIDTempFiles(t *testing.T, dir string, want int) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != want {
		t.Fatalf("ID validator temp files: got %d want %d: %v", len(entries), want, entries)
	}
}

func TestR07S9SmallIDValidationNeverTouchesDisk(t *testing.T) {
	for _, count := range []int{0, 1, 7, 1024, idSortChunk} {
		t.Run(fmt.Sprint(count), func(t *testing.T) {
			tmp := t.TempDir()
			// A broken temporary directory proves the small path never calls
			// CreateTemp, not merely that it deletes files afterward.
			t.Setenv("TMPDIR", filepath.Join(tmp, "missing"))
			ids, err := newDiskIDs(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			defer ids.Close()
			for i := count - 1; i >= 0; i-- {
				if err := ids.Add(uint64(i)); err != nil {
					t.Fatal(err)
				}
			}
			if err := ids.Validate(context.Background()); err != nil {
				t.Fatal(err)
			}
			if err := ids.Validate(context.Background()); err == nil {
				t.Fatal("validation should not repeat after finalization")
			}
		})
	}
}

func TestR07S9DuplicateWithinSmallAndAcrossRuns(t *testing.T) {
	for _, tc := range []struct {
		name      string
		count     int
		duplicate uint64
	}{
		{"in-memory", 17, 9},
		{"cross-chunk", idSortChunk + 100, 11},
		{"cross-carry-level", (discoveryMergeFanIn + 1) * idSortChunk, 11},
	} {
		t.Run(tc.name, func(t *testing.T) {
			temp := t.TempDir()
			t.Setenv("TMPDIR", temp)
			ids, _ := newDiskIDs(context.Background())
			for i := 0; i < tc.count; i++ {
				if err := ids.Add(uint64(i)); err != nil {
					t.Fatal(err)
				}
			}
			err := ids.Add(tc.duplicate)
			if err == nil {
				err = ids.Validate(context.Background())
			}
			if err == nil || !strings.Contains(err.Error(), "duplicate mutation ID") {
				t.Fatalf("expected duplicate: %v", err)
			}
			ids.Close()
			assertIDTempFiles(t, temp, 0)
		})
	}
}

func TestR07S9ExternalIDRunsAreBoundedAndCleaned(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("TMPDIR", tmp)
	ids, _ := newDiskIDs(context.Background())
	// Force more than 16 runs; sorted runs should be merged as they are
	// produced instead of accumulating unbounded path metadata.
	for i := 0; i < (discoveryMergeFanIn*2+3)*idSortChunk; i++ {
		if err := ids.Add(uint64((discoveryMergeFanIn*2+3)*idSortChunk - i)); err != nil {
			t.Fatal(err)
		}
	}
	if len(ids.levels) > 3 {
		t.Fatalf("unexpected run levels: %d", len(ids.levels))
	}
	n := 0
	for _, level := range ids.levels {
		n += len(level)
	}
	if n >= discoveryMergeFanIn*2 {
		t.Fatalf("run metadata grew: %d", n)
	}
	if err := ids.Validate(context.Background()); err != nil {
		t.Fatal(err)
	}
	ids.Close()
	assertIDTempFiles(t, tmp, 0)
}

func TestR07S9CanceledValidationAndSpillFailureCleanUp(t *testing.T) {
	t.Run("cancel-before-validation", func(t *testing.T) {
		tmp := t.TempDir()
		t.Setenv("TMPDIR", tmp)
		ctx, cancel := context.WithCancel(context.Background())
		ids, _ := newDiskIDs(ctx)
		for i := 0; i < idSortChunk+1; i++ {
			if err := ids.Add(uint64(i)); err != nil {
				t.Fatal(err)
			}
		}
		cancel()
		if err := ids.Validate(context.Background()); !errors.Is(err, context.Canceled) {
			t.Fatalf("want canceled: %v", err)
		}
		ids.Close()
		assertIDTempFiles(t, tmp, 0)
	})
	t.Run("spill-error", func(t *testing.T) {
		temp := t.TempDir()
		t.Setenv("TMPDIR", filepath.Join(temp, "missing"))
		ids, _ := newDiskIDs(context.Background())
		defer ids.Close()
		for i := 0; i < idSortChunk; i++ {
			if err := ids.Add(uint64(i)); err != nil {
				t.Fatal(err)
			}
		}
		if err := ids.Add(0); err == nil {
			t.Fatal("missing temporary directory must fail")
		}
	})
}

func BenchmarkR07S9IDValidation(b *testing.B) {
	for _, n := range []int{128, 8192, 16384, 131072} {
		b.Run(fmt.Sprint(n), func(b *testing.B) {
			tmp := b.TempDir()
			b.Setenv("TMPDIR", tmp)
			b.ReportAllocs()
			b.ResetTimer()
			for k := 0; k < b.N; k++ {
				ids, err := newDiskIDs(context.Background())
				if err != nil {
					b.Fatal(err)
				}
				for i := n - 1; i >= 0; i-- {
					if err := ids.Add(uint64(i)); err != nil {
						b.Fatal(err)
					}
				}
				if err := ids.Validate(context.Background()); err != nil {
					b.Fatal(err)
				}
				ids.Close()
			}
		})
	}
}
