// Package d's regular (non-test) source never imports package a -- that
// edge exists only in this package's external test file
// (classify_external_test.go, "package d_test"). That is the whole
// point of this fixture: a naive walk of ordinary build Imports would
// never find the dependency on a that TestNarrowTestScopeKillsAcrossExternalTestOnlyDependency
// requires --narrow-test-scope to still catch.
package d

// Label exists only to give d a small amount of independent behavior
// of its own, unrelated to a.
func Label() string { return "d" }
