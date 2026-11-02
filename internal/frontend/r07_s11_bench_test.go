package frontend

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func BenchmarkR07S11MultiPass(b *testing.B) {
	root := b.TempDir()
	var s strings.Builder
	s.WriteString("package p\n")
	for i := 0; i < 24000; i++ {
		fmt.Fprintf(&s, "func f%d(x int) bool{return x >= %d}\n", i, i)
	}
	if err := os.WriteFile(filepath.Join(root, "p.go"), []byte(s.String()), 0600); err != nil {
		b.Fatal(err)
	}
	opts := Options{Operators: map[string]bool{"boundary": true, "relational": true}}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		sp, _, err := DiscoverToDisk(context.Background(), root, []string{"p.go"}, opts)
		if err != nil {
			b.Fatal(err)
		}
		if err := sp.Close(); err != nil {
			b.Fatal(err)
		}
	}
}
