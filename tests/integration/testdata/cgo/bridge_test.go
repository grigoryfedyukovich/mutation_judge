package cgo

import "testing"

// TestAddOneViaC covers both sides of the sum > 0 boundary
// (n = -1 -> sum = 0 -> false; n = 0 -> sum = 1 -> true), so mutating
// > to >= is caught.
func TestAddOneViaC(t *testing.T) {
	if !AddOneViaC(0) {
		t.Fatal("AddOneViaC(0) = false, want true")
	}
	if AddOneViaC(-1) {
		t.Fatal("AddOneViaC(-1) = true, want false")
	}
}
