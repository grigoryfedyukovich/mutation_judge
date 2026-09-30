// Package assertions is a fixture for TestAssertionAttributionEndToEnd.
// It holds two boundary mutants with deliberately different fates:
//
//   - Over's `n > limit` is killed by a test whose t.Errorf fires, so
//     the KILLED result must name that assertion (file, line, text).
//   - Scan's `n > 0` is covered by a test that only compares the
//     returned error -- the shape of google/uuid's TestNullUUIDScan,
//     which "covers" Scan yet never reads .Valid. That mutant must
//     SURVIVE with no assertion attached: nothing failed, so there is
//     nothing to attribute, and none may be invented.
package assertions

// Over reports whether n exceeds limit.
func Over(n, limit int) bool { return n > limit }
