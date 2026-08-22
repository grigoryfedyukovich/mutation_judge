package unrelated

import (
	"testing"
	"time"
)

// TestSlow is the deliberately slow, irrelevant test: 3 seconds is long
// enough to be an unambiguous signal in the mutants-phase timing
// assertion (which checks a 3000ms threshold) without making the test
// suite painfully slow.
func TestSlow(t *testing.T) {
	time.Sleep(3 * time.Second)
	Noop()
}
