package boundary

// CountPositive records calls only for positive values.
func CountPositive(n int, process func(int)) {
	if n > 0 {
		process(n)
	}
}
