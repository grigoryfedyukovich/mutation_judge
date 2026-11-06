package mutantspool

import (
	"fmt"
	"io"
	"strings"
	"testing"
)

func BenchmarkR07S15FinalCompactDecode(b *testing.B) {
	for _, count := range []int{4096, 32768} {
		spool, err := NewFinalCompact()
		if err != nil {
			b.Fatal(err)
		}
		for i := 0; i < count; i++ {
			item := compactFixture(i)
			item.ID = fmt.Sprintf("M-%012x", i)
			item.Diff = strings.Repeat("x", 480)
			if err := spool.Append(item); err != nil {
				b.Fatal(err)
			}
		}
		b.Run(fmt.Sprintf("records-%d", count), func(b *testing.B) {
			b.ReportAllocs()
			b.ResetTimer()
			for n := 0; n < b.N; n++ {
				r, err := spool.Reader()
				if err != nil {
					b.Fatal(err)
				}
				for i := 0; i < count; i++ {
					m, err := r.Next()
					if err != nil || m.Span.StartByte != i*43 {
						b.Fatalf("record %d: %v", i, err)
					}
				}
				if _, err := r.Next(); err != io.EOF {
					b.Fatalf("unexpected tail %v", err)
				}
				if err := r.Close(); err != nil {
					b.Fatal(err)
				}
			}
		})
		if err := spool.Close(); err != nil {
			b.Fatal(err)
		}
	}
}
