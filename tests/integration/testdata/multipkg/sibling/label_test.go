package sibling

import "testing"

func TestLabel(t *testing.T) {
	if got := Label(); got != "sibling" {
		t.Fatalf("Label() = %q, want %q", got, "sibling")
	}
}
