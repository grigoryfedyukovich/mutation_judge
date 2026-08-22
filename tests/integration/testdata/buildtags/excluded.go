//go:build neverbuildtag

// This file carries a build tag ("neverbuildtag") that is never passed
// via -tags, so go list never places it in GoFiles for any ordinary
// build. It contains an otherwise-mutable comparison specifically to
// prove that a build-tag-excluded file contributes zero mutants, even
// though the AST inside it is perfectly mutable on its own.
package buildtags

func Excluded(n int) bool { return n > 0 }
