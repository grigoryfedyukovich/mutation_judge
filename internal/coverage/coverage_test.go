package coverage

import (
	"os"
	"path/filepath"
	"testing"
)

func TestCoveredUsesPrebuiltSuffixIndex(t *testing.T) {
	path := filepath.Join(t.TempDir(), "coverage.out")
	data := "mode: count\nexample.test/project/internal/p.go:7.1,9.2 1 1\n"
	if err := os.WriteFile(path, []byte(data), 0o644); err != nil {
		t.Fatal(err)
	}
	m, err := Parse(path, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	covered, known := m.Covered("internal/p.go", 8, 8)
	if !known || !covered {
		t.Fatalf("suffix lookup failed: covered=%v known=%v", covered, known)
	}
}

func TestAmbiguousSuffixIsUnknown(t *testing.T) {
	m := Map{
		Lines: map[string]map[int]bool{
			"a/shared/p.go": {1: true},
			"b/shared/p.go": {2: true},
		},
		BySuffix:  map[string]map[int]bool{},
		ambiguous: map[string]bool{},
	}
	m.buildSuffixIndex()
	if _, known := m.Covered("shared/p.go", 1, 1); known {
		t.Fatal("ambiguous suffix should not resolve")
	}
}

// Two distinct files can coincidentally cover the exact same set of line
// numbers (e.g. two small functions with identical statement/block shape,
// as happens for real between examples/boundary/counter.go and
// examples/boundary_fixed/counter.go). Ambiguity must be decided by file
// identity, not by whether the covered-line sets happen to be equal;
// otherwise the index can silently resolve a query to the wrong file.
func TestAmbiguousSuffixIsUnknownEvenWithIdenticalLineSets(t *testing.T) {
	m := Map{
		Lines: map[string]map[int]bool{
			"pkgA/shared/p.go": {1: true, 2: true},
			"pkgB/shared/p.go": {1: true, 2: true},
		},
		BySuffix:  map[string]map[int]bool{},
		ambiguous: map[string]bool{},
	}
	m.buildSuffixIndex()
	if _, known := m.Covered("shared/p.go", 1, 1); known {
		t.Fatal("suffix shared by two distinct files must stay ambiguous even when their covered lines coincide")
	}
}
