package coverage

import (
	"os"
	"path/filepath"
	"testing"
)

func mustParse(t *testing.T, data string) Map {
	t.Helper()
	path := filepath.Join(t.TempDir(), "coverage.out")
	if err := os.WriteFile(path, []byte(data), 0o644); err != nil {
		t.Fatal(err)
	}
	m, err := Parse(path, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func TestPerTestCoveringTestsUnionsAcrossTests(t *testing.T) {
	var p PerTest
	p.Add("TestA", mustParse(t, "mode: count\nexample.test/project/internal/p.go:7.1,9.2 1 1\n"))
	p.Add("TestB", mustParse(t, "mode: count\nexample.test/project/internal/p.go:20.1,22.2 1 1\n"))
	p.Add("TestC", mustParse(t, "mode: count\nexample.test/project/internal/other.go:1.1,3.2 1 1\n"))

	tests, known := p.CoveringTests("internal/p.go", 8, 8)
	if !known {
		t.Fatal("expected a known result")
	}
	if len(tests) != 1 || tests[0] != "TestA" {
		t.Fatalf("expected only TestA, got %v", tests)
	}

	tests, known = p.CoveringTests("internal/other.go", 2, 2)
	if !known || len(tests) != 1 || tests[0] != "TestC" {
		t.Fatalf("expected only TestC, got %v known=%v", tests, known)
	}
}

// TestPerTestCoveringTestsUnionsAcrossSpanLines confirms a multi-line
// mutation span (e.g. a whole case clause or loop body deleted) counts
// a test as covering it if that test reached *any* line in the span,
// not only if it reached every line -- deleting the whole block can
// affect a test that only ever exercised part of it.
func TestPerTestCoveringTestsUnionsAcrossSpanLines(t *testing.T) {
	var p PerTest
	p.Add("TestFirstLine", mustParse(t, "mode: count\nexample.test/project/internal/p.go:10.1,10.5 1 1\n"))
	p.Add("TestLastLine", mustParse(t, "mode: count\nexample.test/project/internal/p.go:14.1,14.5 1 1\n"))
	p.Add("TestUnrelated", mustParse(t, "mode: count\nexample.test/project/internal/p.go:99.1,99.5 1 1\n"))

	tests, known := p.CoveringTests("internal/p.go", 10, 14)
	if !known {
		t.Fatal("expected a known result")
	}
	if len(tests) != 2 || tests[0] != "TestFirstLine" || tests[1] != "TestLastLine" {
		t.Fatalf("expected TestFirstLine and TestLastLine, got %v", tests)
	}
}

func TestPerTestCoveringTestsDeduplicates(t *testing.T) {
	var p PerTest
	m := mustParse(t, "mode: count\nexample.test/project/internal/p.go:7.1,9.2 1 1\nexample.test/project/internal/p.go:20.1,22.2 1 1\n")
	p.Add("TestA", m)
	tests, known := p.CoveringTests("internal/p.go", 7, 22)
	if !known || len(tests) != 1 || tests[0] != "TestA" {
		t.Fatalf("expected TestA exactly once, got %v known=%v", tests, known)
	}
}

// TestPerTestCoveringTestsUnknownWhenEmpty confirms an empty PerTest
// (no test data recorded at all -- coverage-test selection off, or
// profiling failed for everything) reports known=false, distinct from
// a confidently-empty result. A nil *PerTest must behave the same way.
func TestPerTestCoveringTestsUnknownWhenEmpty(t *testing.T) {
	var p PerTest
	if _, known := p.CoveringTests("internal/p.go", 1, 1); known {
		t.Fatal("expected known=false for a PerTest with no recorded data")
	}
	var nilP *PerTest
	if _, known := nilP.CoveringTests("internal/p.go", 1, 1); known {
		t.Fatal("expected known=false for a nil *PerTest")
	}
}

func TestPerTestCoveringTestsConfidentlyEmpty(t *testing.T) {
	var p PerTest
	p.Add("TestA", mustParse(t, "mode: count\nexample.test/project/internal/p.go:7.1,9.2 1 1\n"))
	tests, known := p.CoveringTests("internal/p.go", 50, 50)
	if !known {
		t.Fatal("expected known=true: this is a real, profiled package, just not covering this line")
	}
	if len(tests) != 0 {
		t.Fatalf("expected no covering tests, got %v", tests)
	}
}
