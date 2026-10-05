// Package returnvalue is a fixture for TestReturnValueEndToEnd: one
// result a test checks, and one it calls but never reads.
package returnvalue

// Scale doubles n.
func Scale(n int) int { return n * 2 }

// Count returns the number of elements in xs.
func Count(xs []int) int { return len(xs) }
