package mutantspool

import (
	"fmt"
	"strings"
	"testing"

	"github.com/example/mutation-judge/internal/model"
)

// BenchmarkR07S16CompactAppend measures the internal compact writer alone,
// including the required spill and cleanup, but not decoding or test execution.
func BenchmarkR07S16CompactAppend(b *testing.B) {
	for _, count := range []int{128, 4096} {
		items := make([]model.Mutation, count)
		for i := range items {
			items[i] = model.Mutation{
				ID: fmt.Sprintf("M-%012x", i), Operator: "boundary", RuleID: "relational-boundary",
				Span:     model.Span{File: "package/path/p.go", StartByte: i * 32, EndByte: i*32 + 2, StartLine: i + 1, StartCol: 8, EndLine: i + 1, EndCol: 10},
				Original: ">", Replacement: ">=", Description: "Boundary comparison", Suggestion: "Test the exact limit",
				Diff: fmt.Sprintf("@@ -%d +%d @@\n-%s\n+%s\n", i+1, i+1, strings.Repeat("x", 300), strings.Repeat("y", 300)),
			}
		}
		b.Run(fmt.Sprintf("records-%d", count), func(b *testing.B) {
			b.ReportAllocs()
			for n := 0; n < b.N; n++ {
				spool, err := NewFinalCompact()
				if err != nil {
					b.Fatal(err)
				}
				for _, m := range items {
					if err := spool.Append(m); err != nil {
						b.Fatal(err)
					}
				}
				if err := spool.Close(); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
