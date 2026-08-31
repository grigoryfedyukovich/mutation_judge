// Package broken is the mutated half of the fixture for
// TestMultiPackageCompileFailureIsInvalidNotKilledEndToEnd. Greeting has
// exactly one mutable span for the arithmetic operator: the `+` in the
// string concatenation. Replacing it with `-` (arithmetic's only
// rewrite for token.ADD) does not type-check for strings, so that
// mutant is compile-invalid -- the same documented case as
// examples/arithmetic's own Greeting function. There is deliberately no
// other comparison, boolean literal, or arithmetic operator anywhere in
// this package, so --operators arithmetic discovers exactly one mutant
// here and none in sibling/, keeping the analysis in the end-to-end
// test down to a single, unambiguous result.
package broken

func Greeting(name string) string {
	return "hello, " + name
}
