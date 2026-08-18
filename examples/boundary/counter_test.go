package boundary

import "testing"

// Deliberately omits n == 0 so the > to >= mutant survives.
func TestCountPositive(t *testing.T) {
	calls := 0
	CountPositive(2, func(int) { calls++ })
	CountPositive(-1, func(int) { calls++ })
	if calls != 1 {
		t.Fatalf("calls = %d, want 1", calls)
	}
}
