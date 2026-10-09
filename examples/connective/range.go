package connective

// InRange reports whether n lies in [lo, hi].
func InRange(n, lo, hi int) bool { return n >= lo && n <= hi }

// Eligible reports whether someone may join: an adult member.
func Eligible(age int, member bool) bool { return age >= 18 && member }
