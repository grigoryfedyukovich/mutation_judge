package loopop

import "testing"

func TestSum(t *testing.T) {
	if got := Sum([]int{1, 2, 3}); got != 6 {
		t.Fatalf("Sum([1,2,3]) = %d, want 6", got)
	}
}

// Deliberately tests only the empty-slice case, whose expected result
// (0) is exactly what you'd also get if the loop body never ran at
// all -- so the range-loop mutant survives.
func TestMaxEmpty(t *testing.T) {
	if got := Max(nil); got != 0 {
		t.Fatalf("Max(nil) = %d, want 0", got)
	}
}
