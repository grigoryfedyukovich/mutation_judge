package discarded

import "testing"

func TestLevel(t *testing.T) {
	if got := Level(3); got != 4 {
		t.Errorf("Level(3) = %d, want 4", got)
	}
}
