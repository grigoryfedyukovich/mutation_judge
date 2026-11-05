package frontend

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestR07S14FinalDiscoveryUsesCompactSpool(t *testing.T) {
	temp := t.TempDir()
	t.Setenv("TMPDIR", temp)
	root := t.TempDir()
	var b strings.Builder
	b.WriteString("package p\n")
	for i := 0; i < 110; i++ {
		fmt.Fprintf(&b, "func F%d(x int) bool { return x > %d }\n", i, i)
	}
	if err := os.WriteFile(filepath.Join(root, "p.go"), []byte(b.String()), 0600); err != nil {
		t.Fatal(err)
	}
	opts := Options{Operators: map[string]bool{"boundary": true}}
	expected, err := Discover(root, []string{"p.go"}, opts)
	if err != nil {
		t.Fatal(err)
	}
	spool, total, err := DiscoverToDisk(context.Background(), root, []string{"p.go"}, opts)
	if err != nil {
		t.Fatal(err)
	}
	defer spool.Close()
	if total != len(expected) {
		t.Fatalf("count %d != %d", total, len(expected))
	}
	entries, err := os.ReadDir(temp)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || !strings.HasPrefix(entries[0].Name(), "mutation-judge-mutants-") || !strings.HasSuffix(entries[0].Name(), ".bin") {
		t.Fatalf("expected one private compact final spool, got %v", entries)
	}
	r, err := spool.Reader()
	if err != nil {
		t.Fatal(err)
	}
	for i, want := range expected {
		got, err := r.Next()
		if err != nil || !reflect.DeepEqual(got, want) {
			t.Fatalf("mutation %d: got %+v, want %+v, err %v", i, got, want, err)
		}
	}
	if _, err := r.Next(); err != io.EOF {
		t.Fatalf("expected EOF, got %v", err)
	}
	if err := r.Close(); err != nil {
		t.Fatal(err)
	}
	if err := spool.Close(); err != nil {
		t.Fatal(err)
	}
	entries, err = os.ReadDir(temp)
	if err != nil || len(entries) != 0 {
		t.Fatalf("leaked spool %v: %v", entries, err)
	}
}
