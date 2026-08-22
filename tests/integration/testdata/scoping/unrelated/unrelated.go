// Package unrelated has no relationship whatsoever to a or d. Its test
// (see unrelated_test.go) sleeps 3 seconds specifically so
// TestNarrowTestScopeKillsAcrossExternalTestOnlyDependency and
// TestWithoutNarrowTestScopeRunsTheSlowUnrelatedPackage have an
// unambiguous, non-flaky timing signal for whether --narrow-test-scope
// actually narrowed the mutant-execution test pattern set, rather than
// safely doing nothing.
package unrelated

func Noop() {}
