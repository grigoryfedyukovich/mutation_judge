package errorreturn

import "testing"

// Deliberately checks only the returned value on the missing-key path,
// never that the error is actually propagated -- so the errorreturn
// mutant (replacing err with nil) survives.
func TestLookupMissing(t *testing.T) {
	if v, _ := Lookup(map[string]int{}, "missing"); v != 0 {
		t.Fatalf("Lookup(missing) = %d, want 0", v)
	}
}

func TestLookupFound(t *testing.T) {
	v, err := Lookup(map[string]int{"a": 1}, "a")
	if err != nil || v != 1 {
		t.Fatalf("Lookup(a) = (%d, %v), want (1, nil)", v, err)
	}
}
