// Package a is part of the fixture for
// TestNarrowTestScopeKillsAcrossExternalTestOnlyDependency and
// TestWithoutNarrowTestScopeRunsTheSlowUnrelatedPackage: its own test
// deliberately misses the n == 100/101 boundary that distinguishes
// Classify's `n > 100` from a `n >= 100` mutant, so a's own test can
// never kill it. Only package d's external test file
// (../d/d_test.go) exercises that boundary.
package a

// Classify buckets n into two labels, with the boundary at n > 100.
func Classify(n int) string {
	if n > 100 {
		return "big"
	}
	return "small"
}
