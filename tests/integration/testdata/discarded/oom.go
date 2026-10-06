// Package discarded is a fixture for TestDiscardedResultEndToEnd: an
// unexported function whose first result every caller throws away.
package discarded

func oom(n int) (int, int) { return n * 2, n + 1 }

// Level returns oom's second result and discards its first.
func Level(n int) int {
	_, m := oom(n)
	return m
}
