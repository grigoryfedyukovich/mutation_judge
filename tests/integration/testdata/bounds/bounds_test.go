package bounds

import "testing"

func TestHead(t *testing.T) {
	if got := Head([]int{1, 2, 3}, 2); len(got) != 2 {
		t.Errorf("len(Head) = %d, want 2", len(got))
	}
}

// TestTailRuns covers Tail but never looks at what it returns.
func TestTailRuns(t *testing.T) {
	_ = Tail([]int{1, 2}, 1)
}
