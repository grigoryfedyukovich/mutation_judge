package incremental

import "testing"

func TestIdentifierStart(t *testing.T) {
	for _, r := range []rune{'_', 'a', 'z'} {
		if !IsIdentifierStart(r) {
			t.Fatalf("%q should start an identifier", r)
		}
	}
	if IsIdentifierStart('0') {
		t.Fatal("digit must not start an identifier")
	}
}
