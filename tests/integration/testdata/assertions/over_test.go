package assertions

import "testing"

// TestOverAtLimit pins the boundary: exactly at the limit is not over.
// Mutating `>` to `>=` makes Over(5, 5) true, which fires the t.Errorf
// below -- the assertion the report must attribute the kill to.
func TestOverAtLimit(t *testing.T) {
	if got := Over(5, 5); got {
		t.Errorf("Over(5, 5) = %v, want false", got)
	}
}
