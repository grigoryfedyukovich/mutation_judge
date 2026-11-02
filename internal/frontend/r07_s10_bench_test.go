package frontend

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// BenchmarkR07S10DiscoveryMerge exercises multiple sorting/merge passes,
// including the temporary mutation serialization paths.
func BenchmarkR07S10DiscoveryMerge(b *testing.B) {
	root := b.TempDir()
	var src strings.Builder
	src.WriteString("package p\n")
	for i := 0; i < 4600; i++ {
		fmt.Fprintf(&src, "func f%d(x int) bool { return x >= %d }\n", i, i)
	}
	if err := os.WriteFile(filepath.Join(root, "p.go"), []byte(src.String()), 0600); err != nil {
		b.Fatal(err)
	}
	opts := Options{Operators: map[string]bool{"boundary": true, "relational": true}}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		spool, _, err := DiscoverToDisk(context.Background(), root, []string{"p.go"}, opts)
		if err != nil {
			b.Fatal(err)
		}
		if err := spool.Close(); err != nil {
			b.Fatal(err)
		}
	}
}
