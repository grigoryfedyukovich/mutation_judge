package loopop

// Sum adds up xs.
func Sum(xs []int) int {
	total := 0
	for i := 0; i < len(xs); i++ {
		total += xs[i]
	}
	return total
}

// Max returns the largest value in xs, treating negatives as 0, or 0
// if xs is empty.
func Max(xs []int) int {
	best := 0
	for _, x := range xs {
		if x < 0 {
			x = 0
		}
		if x > best {
			best = x
		}
	}
	return best
}
