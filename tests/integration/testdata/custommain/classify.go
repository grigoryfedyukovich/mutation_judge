// Package custommain is a fixture for TestCustomTestMainEndToEnd: it
// proves a package whose TestMain calls os.Exit with a nonzero status
// (bypassing the normal per-test reporting entirely) is still classified
// KILLED, with no test incorrectly attributed, rather than falling
// through to UNKNOWN or INVALID.
package custommain

func Classify(n int) bool { return n > 0 }
