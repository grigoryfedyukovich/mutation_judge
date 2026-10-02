package deadstore

import "strings"

// HasComma reports whether s contains a comma.
//
// hasComma's initializer is never observed: the very next statement is
// an if/else that assigns it on every path, and its condition does not
// read it. Flipping the initializer's false to true therefore cannot
// change any result, and the boolean operator classifies that mutant
// EQUIVALENT instead of running it (see this example's README). The two
// assignments inside the branches are ordinary boolean mutants, and the
// tests kill both.
func HasComma(s string) bool {
	hasComma := false
	if strings.Contains(s, ",") {
		hasComma = true
	} else {
		hasComma = false
	}
	return hasComma
}
