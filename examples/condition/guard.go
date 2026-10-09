package condition

// Abs returns the absolute value of n.
func Abs(n int) int {
	if n < 0 {
		return -n
	}
	return n
}

// Open reports that name was opened, if anyone is listening.
func Open(name string, trace func(string)) {
	if trace != nil {
		trace("open " + name)
	}
}
