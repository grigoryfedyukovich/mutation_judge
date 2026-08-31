// Package sibling has no relationship whatsoever to broken -- it does
// not import it, and nothing here imports sibling either. That is the
// whole point of this fixture: proving that sibling's own test starting
// and passing under a shared `./...` pattern set must not mask a build
// failure in the unrelated broken package. Label deliberately contains
// no comparison, boolean literal, or arithmetic operator of its own, so
// no operator discovers any mutant here -- the end-to-end test's single
// mutant comes entirely from broken/greeting.go.
package sibling

func Label() string { return "sibling" }
