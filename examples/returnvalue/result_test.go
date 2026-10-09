package returnvalue

import "testing"

func TestScale(t *testing.T) {
	if got := Scale(3); got != 6 {
		t.Errorf("Scale(3) = %d, want 6", got)
	}
}

// TestCountRuns covers Count but never looks at what it returns.
func TestCountRuns(t *testing.T) {
	_ = Count([]int{1, 2})
}
