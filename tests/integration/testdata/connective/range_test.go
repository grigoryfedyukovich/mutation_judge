package connective

import "testing"

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
