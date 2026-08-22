// package d_test is an *external* test package for d: this is the only
// place anywhere in this fixture that imports package a. go list -deps
// -test attributes this dependency to package d's test target even
// though d's own regular source (d.go) never imports a, which is
// exactly what --narrow-test-scope must still catch when scoping a
// mutant in a.
package d_test

import (
	"testing"

	"github.com/example/mutation-judge/tests/integration/testdata/scoping/a"
)

// TestClassifyBoundary exercises exactly the n == 100/101 boundary a's
// own test omits, so this is the only test anywhere that kills a's
// n > 100 -> n >= 100 mutant.
func TestClassifyBoundary(t *testing.T) {
	if a.Classify(100) != "small" {
		t.Fatal(`a.Classify(100) != "small"`)
	}
	if a.Classify(101) != "big" {
		t.Fatal(`a.Classify(101) != "big"`)
	}
}
