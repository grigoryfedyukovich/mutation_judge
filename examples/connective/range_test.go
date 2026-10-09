package connective

import "testing"

// TestInRange includes a case on each side of the range, so exactly one
// operand is true in two of the cases.
func TestInRange(t *testing.T) {
	cases := []struct {
		n    int
		want bool
	}{
		{5, true},
		{0, false},
		{11, false},
	}
	for _, c := range cases {
		if got := InRange(c.n, 1, 10); got != c.want {
			t.Errorf("InRange(%d, 1, 10) = %v, want %v", c.n, got, c.want)
		}
	}
}

// TestEligible only tries both-true and both-false, so && and || agree
// on every input it uses.
func TestEligible(t *testing.T) {
	if !Eligible(20, true) {
		t.Error("Eligible(20, true) = false, want true")
	}
	if Eligible(10, false) {
		t.Error("Eligible(10, false) = true, want false")
	}
}
