// Package initpanic is a fixture for TestPackageInitPanicEndToEnd: it
// proves a package that fails before any test can run for a runtime
// reason (here, a panic in init()) is classified KILLED rather than
// INVALID -- the package compiled fine, so this is a real mutant kill,
// not an uncompilable mutant.
//
// init() re-checks, at package-initialization time, the same invariant
// the mutable comparison in valid() encodes: 0 must not be classified
// positive. That holds for the original `n > 0` and init() stays quiet;
// mutating it to `n >= 0` flips valid(0) to true and init() panics
// before any test ever runs.
package initpanic

func init() {
	if valid(0) {
		panic("invariant violated: 0 must not be classified positive")
	}
}

func valid(n int) bool { return n > 0 }
