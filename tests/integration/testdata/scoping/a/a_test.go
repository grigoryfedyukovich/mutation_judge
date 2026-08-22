package a

import "testing"

// TestClassify deliberately omits n == 100/101, mirroring
// examples/boundary/counter_test.go's survivor pattern: a's own test
// suite can never kill the n > 100 -> n >= 100 mutant on its own.
func TestClassify(t *testing.T) {
	if Classify(50) != "small" {
		t.Fatal(`Classify(50) != "small"`)
	}
	if Classify(200) != "big" {
		t.Fatal(`Classify(200) != "big"`)
	}
}
