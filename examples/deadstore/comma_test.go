package deadstore

import "testing"

func TestHasComma(t *testing.T) {
	if !HasComma("a,b") {
		t.Error(`HasComma("a,b") = false, want true`)
	}
	if HasComma("ab") {
		t.Error(`HasComma("ab") = true, want false`)
	}
}
