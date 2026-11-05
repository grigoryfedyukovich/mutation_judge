package mutantspool

import (
	"fmt"
	"io"
	"strings"
	"testing"

	"github.com/example/mutation-judge/internal/model"
)

// BenchmarkR07S14FinalSpool measures the entire append/read/cleanup cycle.
// Both cases use identical mutations and the same number of disk-spilled records.
func BenchmarkR07S14FinalSpool(b *testing.B) {
	for _, count := range []int{128, 4096} {
		items := make([]model.Mutation, count)
		for i := range items {
			items[i] = model.Mutation{
				ID: fmt.Sprintf("M-%012x", i), Operator: "boundary", RuleID: "relational-boundary",
				Span:     model.Span{File: "package/path/p.go", StartByte: i * 32, EndByte: i*32 + 2, StartLine: i + 1, StartCol: 8, EndLine: i + 1, EndCol: 10},
				Original: ">", Replacement: ">=", Description: "Boundary comparison", Suggestion: "Test the exact limit",
				Diff:             fmt.Sprintf("@@ -%d +%d @@\n-%s\n+%s\n", i+1, i+1, strings.Repeat("x", 300), strings.Repeat("y", 300)),
				EquivalentReason: "",
			}
		}
		for _, tc := range []struct {
			name     string
			newSpool func() (*Spool, error)
		}{
			{"JSON", New},
			{"Compact", NewCompact},
		} {
			b.Run(fmt.Sprintf("%s-%d", tc.name, count), func(b *testing.B) {
				b.ReportAllocs()
				for n := 0; n < b.N; n++ {
					s, err := tc.newSpool()
					if err != nil {
						b.Fatal(err)
					}
					for i := range items {
						if err := s.Append(items[i]); err != nil {
							b.Fatal(err)
						}
					}
					r, err := s.Reader()
					if err != nil {
						b.Fatal(err)
					}
					for range items {
						if _, err := r.Next(); err != nil {
							b.Fatal(err)
						}
					}
					if _, err := r.Next(); err != io.EOF {
						b.Fatalf("unexpected tail %v", err)
					}
					if err := r.Close(); err != nil {
						b.Fatal(err)
					}
					if err := s.Close(); err != nil {
						b.Fatal(err)
					}
				}
			})
		}
	}
}
