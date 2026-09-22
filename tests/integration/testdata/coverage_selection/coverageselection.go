// Package coverageselection is built specifically for
// TestCoverageTestSelectionKillsWithoutRunningUnrelatedTest and
// TestWithoutCoverageTestSelectionRunsTheSlowUnrelatedTest:
// TestClassifyBoundary is the only test that reaches Classify's own
// n > 100 boundary; TestSlowUnrelated never calls Classify at all and
// sleeps 3 seconds specifically to give those two tests an
// unambiguous, non-flaky timing signal for whether
// --coverage-test-selection actually narrowed the per-mutant `-run`
// pattern down to TestClassifyBoundary alone, rather than safely
// doing nothing.
package coverageselection

// Classify buckets n into two labels, with the boundary at n > 100.
func Classify(n int) string {
	if n > 100 {
		return "big"
	}
	return "small"
}

// Unrelated has no relationship whatsoever to Classify.
func Unrelated() {}
