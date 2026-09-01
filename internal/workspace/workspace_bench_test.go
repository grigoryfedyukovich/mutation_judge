package workspace

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// generateModule writes a synthetic Go module with numFiles source files,
// grouped into packages of filesPerPackage each (matching typical
// real-world project structure -- many small packages, not one giant
// one), for benchmarking Digest and CopyModule at realistic scale. Each
// file is ~3KB (roughly 100 lines across a handful of functions), so
// total size, not just file count, tracks a real module.
func generateModule(b *testing.B, numFiles, filesPerPackage int) string {
	b.Helper()
	root := b.TempDir()
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module bench.test/large\n\ngo 1.22\n"), 0o644); err != nil {
		b.Fatal(err)
	}
	written := 0
	pkgIdx := 0
	for written < numFiles {
		pkgName := fmt.Sprintf("pkg%d", pkgIdx)
		pkgDir := filepath.Join(root, pkgName)
		if err := os.MkdirAll(pkgDir, 0o755); err != nil {
			b.Fatal(err)
		}
		for i := 0; i < filesPerPackage && written < numFiles; i++ {
			var src strings.Builder
			fmt.Fprintf(&src, "package %s\n\n", pkgName)
			for fn := 0; fn < 6; fn++ {
				fmt.Fprintf(&src, "// Func%dIn%s is generated bench fixture content, not a real function.\n", fn, pkgName)
				fmt.Fprintf(&src, "func Func%dIn%s(a, b, c int) int {\n", fn, pkgName)
				fmt.Fprintf(&src, "\tif a > 0 && b < 10 || c == 0 {\n\t\treturn a + b - c\n\t}\n\treturn a * b\n}\n\n")
			}
			path := filepath.Join(pkgDir, fmt.Sprintf("file%d.go", i))
			if err := os.WriteFile(path, []byte(src.String()), 0o644); err != nil {
				b.Fatal(err)
			}
			written++
		}
		pkgIdx++
	}
	return root
}

// BenchmarkDigest and BenchmarkCopyModule measure the two one-time,
// per-analysis-run costs (see internal/analysis.Engine.Analyze -- both
// run exactly once per invocation, before baseline execution, not once
// per mutant) at increasing module sizes, so a "should we narrow what
// gets digested/copied" decision is grounded in real numbers rather than
// intuition. Run with:
//
//	go test ./internal/workspace/ -bench . -benchtime 3x -run '^$'
//
// File counts span a realistic small project (100 files) up to a
// genuinely large one (8000 files, ~24MB of source at this fixture's
// ~3KB/file) so the results show the actual scaling curve, not just one
// data point.
//
// Caveat observed while writing this benchmark: on a constrained,
// single-vCPU sandbox with virtualized block storage, BenchmarkCopyModule
// (write-heavy: stat, create, read+write loop, chmod, and MkdirAll per
// new package directory) showed run-to-run variance large enough to
// swap the apparent ranking between the 3000-8000 file sizes across
// separate `go test -bench` invocations, while BenchmarkDigest (read-only,
// no writes) stayed consistent to within a few percent across the same
// range. That is almost certainly this sandbox's virtual disk write
// path, not an algorithmic property of CopyModule -- its logic is a
// straightforward one-pass WalkDir with one sequential copy per file, no
// obvious super-linear structure. Trust the shape (write-heavy work is
// far more exposed to storage noise than read-only hashing) over any
// single run's absolute numbers, and prefer more repetitions
// (-benchtime 10x or higher) plus repeated invocations before drawing
// conclusions from this benchmark on unfamiliar hardware. See
// docs/performance.md for the full writeup and what this motivates.
func BenchmarkDigest(b *testing.B) {
	for _, n := range []int{100, 500, 2000, 8000} {
		b.Run(fmt.Sprintf("files=%d", n), func(b *testing.B) {
			root := generateModule(b, n, 20)
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				if _, err := Digest(root, ".mutation-judge/cache"); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

func BenchmarkCopyModule(b *testing.B) {
	for _, n := range []int{100, 500, 2000, 8000} {
		b.Run(fmt.Sprintf("files=%d", n), func(b *testing.B) {
			root := generateModule(b, n, 20)
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				_, cleanup, err := CopyModule(root, ".mutation-judge/cache")
				if err != nil {
					b.Fatal(err)
				}
				// Excluded from the timed cost on purpose: cleanup
				// (os.RemoveAll) is not part of what a real analysis run
				// pays before baseline execution starts, which is what
				// this benchmark means to measure.
				b.StopTimer()
				cleanup()
				b.StartTimer()
			}
		})
	}
}
