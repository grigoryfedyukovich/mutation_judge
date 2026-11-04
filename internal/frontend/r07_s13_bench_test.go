package frontend

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

// BenchmarkR07S13ManySmallFiles measures discovery including temporary-run
// I/O for a module whose mutation count per file fits one sorting chunk.
func BenchmarkR07S13ManySmallFiles(b *testing.B) {
	root := b.TempDir()
	const filesCount = 80
	files := make([]string, filesCount)
	for i := range files {
		files[i] = fmt.Sprintf("file_%03d.go", i)
		source := fmt.Sprintf("package p\nfunc F%d(x int) bool { return x >= %d }\n", i, i)
		if err := os.WriteFile(filepath.Join(root, files[i]), []byte(source), 0600); err != nil {
			b.Fatal(err)
		}
	}
	opts := Options{Operators: map[string]bool{"boundary": true, "relational": true}}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		spool, _, err := DiscoverToDisk(context.Background(), root, files, opts)
		if err != nil {
			b.Fatal(err)
		}
		if err := spool.Close(); err != nil {
			b.Fatal(err)
		}
	}
}
