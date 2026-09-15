package relational

import "testing"

// Deliberately never calls IsFinished, so its != mutant survives;
// only IsPending's == is exercised and killed.
func TestIsPendingDetectsPendingStatus(t *testing.T) {
	if !IsPending(Pending) {
		t.Fatal("IsPending(Pending) = false, want true")
	}
	if IsPending(Running) {
		t.Fatal("IsPending(Running) = true, want false")
	}
}
