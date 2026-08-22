package custommain

import (
	"os"
	"testing"
)

// TestMain re-checks the same invariant Classify's mutable comparison
// encodes -- 0 must not be classified positive -- and calls os.Exit
// directly, before m.Run() is ever reached, if it's violated. That
// means a mutated `n >= 0` never runs a single test: TestNoop, and any
// per-test reporting, are entirely bypassed.
func TestMain(m *testing.M) {
	if Classify(0) {
		os.Exit(1)
	}
	os.Exit(m.Run())
}

func TestNoop(t *testing.T) {}
