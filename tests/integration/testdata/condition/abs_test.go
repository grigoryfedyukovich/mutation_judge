package condition

import "testing"

func TestAbs(t *testing.T) {
	if got := Abs(-3); got != 3 {
		t.Errorf("Abs(-3) = %d, want 3", got)
	}
	if got := Abs(3); got != 3 {
		t.Errorf("Abs(3) = %d, want 3", got)
	}
}
