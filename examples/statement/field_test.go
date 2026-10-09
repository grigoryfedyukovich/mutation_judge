package statement

import "testing"

// TestScan covers every line of Scan and checks the error and the
// value, but never reads Valid. Line coverage cannot see that gap.
func TestScan(t *testing.T) {
	var f Field
	if err := f.Scan("abc"); err != nil {
		t.Fatalf("Scan(abc): %v", err)
	}
	if f.Value != "abc" {
		t.Errorf("Value = %q, want abc", f.Value)
	}
	if err := f.Scan(""); err == nil {
		t.Error("Scan(\"\") = nil, want an error")
	}
}
