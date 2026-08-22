// Package buildtags is a fixture for
// TestBuildTagExcludedFileIsNeverMutatedEndToEnd: discovery must only
// mutate files go list actually places in the current build (GoFiles),
// not every .go file found by walking the package directory.
package buildtags

// Included is always part of the build; its comparison is the one
// mutant this fixture must produce.
func Included(n int) bool { return n > 0 }
