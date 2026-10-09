// Package bounds is a fixture for TestBoundsEndToEnd: one slice bound a
// test checks, and one it only exercises.
package bounds

// Head returns the first n elements of s.
func Head(s []int, n int) []int { return s[:n] }

// Tail returns s without its first k elements.
func Tail(s []int, k int) []int { return s[k:] }
