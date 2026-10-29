package frontend

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/example/mutation-judge/internal/model"
)

func TestR07S6DiskDiscoveryMatchesUnlimited(t *testing.T) {
	root := t.TempDir()
	var b strings.Builder
	b.WriteString("package p\n")
	for i := 0; i < 1100; i++ {
		fmt.Fprintf(&b, "func f%d(x int) bool { return x > %d }\n", i, i)
	}
	if err := os.WriteFile(filepath.Join(root, "b.go"), []byte(b.String()), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "a.go"), []byte("package p\nfunc Z(x int) bool { return x != 7 }\n"), 0600); err != nil {
		t.Fatal(err)
	}
	files := []string{"b.go", "a.go"}
	opts := Options{Operators: map[string]bool{"boundary": true, "relational": true}}
	full, err := Discover(root, files, opts)
	if err != nil {
		t.Fatal(err)
	}
	if len(full) < discoveryChunkSize*2 {
		t.Fatalf("test fixture too small: %d", len(full))
	}
	spool, total, err := DiscoverToDisk(context.Background(), root, files, opts)
	if err != nil {
		t.Fatal(err)
	}
	defer spool.Close()
	if total != len(full) || spool.Len() != len(full) {
		t.Fatalf("counts: %d/%d want %d", total, spool.Len(), len(full))
	}
	reader, err := spool.Reader()
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	for i, want := range full {
		got, err := reader.Next()
		if err != nil {
			t.Fatalf("read %d: %v", i, err)
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("mutation %d mismatch: %+v != %+v", i, got, want)
		}
	}
}

func TestR07S6DiscoveryCleanupAndCancellation(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("TMPDIR", tmp)
	root := t.TempDir()
	write := func(file, text string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(root, file), []byte(text), 0600); err != nil {
			t.Fatal(err)
		}
	}
	write("a.go", "package p\nfunc A(x int) bool {return x > 0}\n")
	write("z.go", "package p\nfunc Bad( {\n")
	opts := Options{Operators: map[string]bool{"boundary": true}}
	if _, _, err := DiscoverToDisk(context.Background(), root, []string{"a.go", "z.go"}, opts); err == nil {
		t.Fatal("expected parse error")
	}
	entries, err := os.ReadDir(tmp)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("leftover temp files after error: %v", entries)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, _, err := DiscoverToDisk(ctx, root, []string{"a.go"}, opts); err == nil {
		t.Fatal("expected cancellation")
	}
	entries, err = os.ReadDir(tmp)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("leftover temp files after cancellation: %v", entries)
	}
}

func TestR07S6ExternalIDValidation(t *testing.T) {
	temp := t.TempDir()
	t.Setenv("TMPDIR", temp)
	ids, err := newDiskIDs()
	if err != nil {
		t.Fatal(err)
	}
	defer ids.Close()
	// More than one on-disk sort chunk, with the duplicate in different chunks.
	for i := 0; i < idSortChunk+10; i++ {
		if err := ids.Add(uint64(i)); err != nil {
			t.Fatal(err)
		}
	}
	if err := ids.Add(3); err != nil {
		t.Fatal(err)
	}
	if err := ids.Validate(context.Background()); err == nil || !strings.Contains(err.Error(), "duplicate mutation ID") {
		t.Fatalf("duplicate not found: %v", err)
	}
	ids.Close()
	entries, _ := os.ReadDir(temp)
	if len(entries) != 0 {
		t.Fatalf("leftover ID sort files: %v", entries)
	}
}

func TestR07S6DiskDiscoveryChangedLines(t *testing.T) {
	root := t.TempDir()
	src := "package p\nfunc F(x int) bool { return x > 0 }\nfunc G(x int) bool { return x > 1 }\n"
	if err := os.WriteFile(filepath.Join(root, "p.go"), []byte(src), 0600); err != nil {
		t.Fatal(err)
	}
	opts := Options{Operators: map[string]bool{"boundary": true}, ChangedLines: map[string]map[int]bool{"p.go": {2: true}}}
	want, err := Discover(root, []string{"p.go"}, opts)
	if err != nil {
		t.Fatal(err)
	}
	spool, n, err := DiscoverToDisk(context.Background(), root, []string{"p.go"}, opts)
	if err != nil {
		t.Fatal(err)
	}
	defer spool.Close()
	if len(want) != 1 || n != 1 {
		t.Fatalf("counts: %d %d", len(want), n)
	}
	r, _ := spool.Reader()
	defer r.Close()
	got, err := r.Next()
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, model.Mutation(want[0])) {
		t.Fatalf("changed lines mismatch: %+v / %+v", got, want[0])
	}
}

func TestR07S6MultipassExternalMerge(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("TMPDIR", tmp)
	root := t.TempDir()
	var source strings.Builder
	source.WriteString("package p\n")
	// Each function yields a boundary candidate, exceeding 16 on-disk
	// chunks and requiring a second merge pass.
	for i := 0; i < 8800; i++ {
		fmt.Fprintf(&source, "func f%d(x int) bool {return x >= %d}\n", i, i)
	}
	if err := os.WriteFile(filepath.Join(root, "p.go"), []byte(source.String()), 0600); err != nil {
		t.Fatal(err)
	}
	opts := Options{Operators: map[string]bool{"boundary": true, "relational": true}}
	full, err := Discover(root, []string{"p.go"}, opts)
	if err != nil {
		t.Fatal(err)
	}
	if len(full) <= discoveryChunkSize*discoveryMergeFanIn {
		t.Fatalf("fixture produced only %d mutations", len(full))
	}
	spool, n, err := DiscoverToDisk(context.Background(), root, []string{"p.go"}, opts)
	if err != nil {
		t.Fatal(err)
	}
	if n != len(full) {
		t.Fatalf("count %d != %d", n, len(full))
	}
	reader, err := spool.Reader()
	if err != nil {
		t.Fatal(err)
	}
	for i, m := range full {
		got, err := reader.Next()
		if err != nil {
			t.Fatal(err)
		}
		if got.ID != m.ID || got.Diff != m.Diff || got.Span != m.Span {
			t.Fatalf("mutation %d differs", i)
		}
	}
	_ = reader.Close()
	_ = spool.Close()
	entries, err := os.ReadDir(tmp)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("temporary files leaked: %v", entries)
	}
}
