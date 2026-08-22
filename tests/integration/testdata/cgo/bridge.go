// Package cgo is a fixture for TestCgoPackageEndToEnd: it proves the
// pipeline -- AST-based mutation discovery, the temporary sandbox
// module copy, and go test execution -- works correctly against a
// package that uses cgo. The plain-Go boundary comparison below must
// still be found by discovery even though this file also has
// `import "C"`, and the sandbox copy must preserve whatever cgo needs
// to compile, not just parse.
package cgo

/*
#include <stdlib.h>

static int c_add_one(int n) {
	return n + 1;
}
*/
import "C"

// AddOneViaC calls into C to increment n, then classifies whether the
// result is positive -- the one mutable comparison this fixture needs.
func AddOneViaC(n int) bool {
	sum := int(C.c_add_one(C.int(n)))
	return sum > 0
}
