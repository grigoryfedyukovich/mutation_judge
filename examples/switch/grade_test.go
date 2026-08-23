package switchop

import "testing"

// Deliberately never exercises the middle "B" case, so deleting that
// case survives: no test's outcome depends on it existing at all.
func TestGradeTopAndBottom(t *testing.T) {
	if got := Grade(95); got != "A" {
		t.Fatalf("Grade(95) = %q, want %q", got, "A")
	}
	if got := Grade(50); got != "F" {
		t.Fatalf("Grade(50) = %q, want %q", got, "F")
	}
}
