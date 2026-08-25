package equivalentop

import "testing"

func TestLessByPriority(t *testing.T) {
	if !Less(Item{Priority: 1}, Item{Priority: 2}) {
		t.Fatal("want true: lower priority sorts first")
	}
}

// TestLessBySubmittedAt covers the SubmittedAt boundary directly --
// equal timestamps must not sort before each other -- which is what
// kills that comparison's boundary mutant. Nothing here needs to
// (or could) exercise the Priority comparison's own boundary,
// because that mutant never runs at all.
func TestLessBySubmittedAt(t *testing.T) {
	a := Item{Priority: 1, SubmittedAt: 5}
	b := Item{Priority: 1, SubmittedAt: 5}
	if Less(a, b) {
		t.Fatal("equal SubmittedAt must not sort before itself")
	}
	if !Less(Item{Priority: 1, SubmittedAt: 4}, Item{Priority: 1, SubmittedAt: 5}) {
		t.Fatal("want true: earlier SubmittedAt sorts first")
	}
}
