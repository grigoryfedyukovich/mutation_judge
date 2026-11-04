package frontend

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/example/mutation-judge/internal/model"
)

func r07S13ReadAll(t *testing.T, root string, files []string, opts Options) []model.Mutation {
	t.Helper()
	spool, count, err := DiscoverToDisk(context.Background(), root, files, opts)
	if err != nil {
		t.Fatal(err)
	}
	defer spool.Close()
	r, err := spool.Reader()
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	var got []model.Mutation
	for {
		m, err := r.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		got = append(got, m)
	}
	if count != len(got) || spool.Len() != count {
		t.Fatalf("reported count %d, spool %d, read %d", count, spool.Len(), len(got))
	}
	return got
}

func TestR07S13ShortFileAvoidsTemporaryCompactRuns(t *testing.T) {
	root := t.TempDir()
	src := "package p\nfunc F(x int) bool { return x >= 1 }\n"
	if err := os.WriteFile(filepath.Join(root, "p.go"), []byte(src), 0600); err != nil {
		t.Fatal(err)
	}
	opts := Options{Operators: map[string]bool{"boundary": true}}
	want, err := Discover(root, []string{"p.go"}, opts)
	if err != nil {
		t.Fatal(err)
	}
	if len(want) == 0 || len(want) >= 64 {
		t.Fatalf("invalid small-file fixture: %d", len(want))
	}
	// Source discovery and small output spools do not require a writable TMPDIR.
	// Creating a compact sorting run would fail here.
	t.Setenv("TMPDIR", filepath.Join(t.TempDir(), "does-not-exist"))
	got := r07S13ReadAll(t, root, []string{"p.go"}, opts)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("disk discovery differed from full discovery: got=%+v want=%+v", got, want)
	}
}

func TestR07S13FastPathAndChunkBoundaryMatchFullDiscovery(t *testing.T) {
	root := t.TempDir()
	opts := Options{Operators: map[string]bool{"boundary": true, "relational": true}}
	var files []string
	for _, count := range []int{0, 1, 31, discoveryChunkSize - 1, discoveryChunkSize, discoveryChunkSize + 1} {
		name := fmt.Sprintf("p_%04d.go", count)
		var src strings.Builder
		src.WriteString("package p\n")
		for i := 0; i < count; i++ {
			fmt.Fprintf(&src, "func F%d_%d(x int) bool { return x >= %d }\n", count, i, i)
		}
		if err := os.WriteFile(filepath.Join(root, name), []byte(src.String()), 0600); err != nil {
			t.Fatal(err)
		}
		files = append(files, name)
	}
	// Reverse discovery input: output must still use filename/offset/ID order.
	for i, j := 0, len(files)-1; i < j; i, j = i+1, j-1 {
		files[i], files[j] = files[j], files[i]
	}
	want, err := Discover(root, files, opts)
	if err != nil {
		t.Fatal(err)
	}
	got := r07S13ReadAll(t, root, files, opts)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("mixed short/chunked discovery mismatch: got %d want %d", len(got), len(want))
	}
}

func TestR07S13ShortFileCancellationAndParseFailure(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "p.go"), []byte("package p\nfunc F(x int) bool { return x >= 1 }\n"), 0600); err != nil {
		t.Fatal(err)
	}
	opts := Options{Operators: map[string]bool{"boundary": true}}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if s, _, err := DiscoverToDisk(ctx, root, []string{"p.go"}, opts); err == nil {
		s.Close()
		t.Fatal("accepted canceled discovery")
	}
	if err := os.WriteFile(filepath.Join(root, "p.go"), []byte("package p\nfunc Bad( {\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if s, _, err := DiscoverToDisk(context.Background(), root, []string{"p.go"}, opts); err == nil {
		s.Close()
		t.Fatal("accepted malformed source")
	}
}
