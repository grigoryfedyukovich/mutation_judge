package initpanic

import "testing"

// TestValidPositive is an ordinary passing test so the baseline run
// exercises real test execution, not just a package with no test files.
func TestValidPositive(t *testing.T) {
	if !valid(5) {
		t.Fatal("valid(5) = false, want true")
	}
}
