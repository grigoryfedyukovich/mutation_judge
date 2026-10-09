package discarded

func oom(n int) (int, int) { return n * 2, n + 1 }

// Level returns oom's second result and throws its first away.
func Level(n int) int {
	_, m := oom(n)
	return m
}
