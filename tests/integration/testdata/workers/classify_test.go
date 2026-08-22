package workers

import "testing"

// TestA and TestB cover n == 0, so A's and B's n > 0 -> n >= 0 mutants
// are killed.
func TestA(t *testing.T) {
	if A(0) {
		t.Fatal("A(0) = true, want false")
	}
}

func TestB(t *testing.T) {
	if B(0) {
		t.Fatal("B(0) = true, want false")
	}
}

// TestC and TestD deliberately omit n == 0 (mirroring
// examples/boundary's survivor pattern), so C's and D's mutants survive
// -- this fixture needs a non-trivial mix of verdicts, not an all-pass
// or all-fail set, for a meaningful sequential-vs-parallel comparison.
func TestC(t *testing.T) {
	if !C(2) || C(-1) {
		t.Fatal("unexpected C result")
	}
}

func TestD(t *testing.T) {
	if !D(2) || D(-1) {
		t.Fatal("unexpected D result")
	}
}
