package coverageselection

import (
	"testing"
)

// TestClassifyBoundary exercises exactly the n == 100/101 boundary
// that distinguishes Classify's `n > 100` from an `n >= 100` mutant --
// the only test anywhere in this package that can kill it.
func TestClassifyBoundary(t *testing.T) {
	if Classify(100) != "small" {
		t.Fatal(`Classify(100) != "small"`)
	}
	if Classify(101) != "big" {
		t.Fatal(`Classify(101) != "big"`)
	}
}

// TestUnrelated never calls Classify at all.
func TestUnrelated(t *testing.T) {
	Unrelated()
}
