// Package condition is a fixture for TestConditionNegationEndToEnd: one
// if statement, one condition mutant.
package condition

// Abs returns the absolute value of n.
func Abs(n int) int {
	if n < 0 {
		return -n
	}
	return n
}
