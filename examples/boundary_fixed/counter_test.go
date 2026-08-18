package boundaryfixed

import "testing"

func TestCountPositive(t *testing.T) {
	tests := []struct {
		name      string
		n         int
		wantCalls int
	}{
		{name: "positive", n: 2, wantCalls: 1},
		{name: "zero", n: 0, wantCalls: 0},
		{name: "negative", n: -1, wantCalls: 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			calls := 0
			CountPositive(tt.n, func(int) { calls++ })
			if calls != tt.wantCalls {
				t.Fatalf("CountPositive(%d) produced %d calls, want %d", tt.n, calls, tt.wantCalls)
			}
		})
	}
}
