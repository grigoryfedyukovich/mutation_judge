package buildtags

import "testing"

func TestIncluded(t *testing.T) {
	if !Included(1) {
		t.Fatal("Included(1) = false, want true")
	}
}
