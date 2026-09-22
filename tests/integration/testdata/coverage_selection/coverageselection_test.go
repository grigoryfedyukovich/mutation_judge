package coverageselection

import (
	"testing"
	"time"
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

// TestSlowUnrelated never calls Classify at all.
func TestSlowUnrelated(t *testing.T) {
	time.Sleep(3 * time.Second)
	Unrelated()
}
