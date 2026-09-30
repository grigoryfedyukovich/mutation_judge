package assertions

import "testing"

// TestScan covers Scan but only compares the error, never Field.Valid,
// so mutating Scan's `n > 0` survives despite full line coverage.
func TestScan(t *testing.T) {
	if _, err := Scan(5); err != nil {
		t.Fatalf("Scan(5): %v", err)
	}
}
