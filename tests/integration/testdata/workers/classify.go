// Package workers is the real-toolchain fixture for
// TestWorkersProducesSameResultsAsSequential: four independent
// boundary-mutable comparisons, two killed and two surviving (see
// classify_test.go), run once sequentially and once with --workers 3.
package workers

func A(n int) bool { return n > 0 }
func B(n int) bool { return n > 0 }
func C(n int) bool { return n > 0 }
func D(n int) bool { return n > 0 }
