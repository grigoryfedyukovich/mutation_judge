package mutantspool

import (
	"github.com/example/mutation-judge/internal/model"
	"io"
	"strings"
	"testing"
)

func BenchmarkR07S8SmallMutations(b *testing.B) {
	items := make([]model.Mutation, 12)
	for i := range items {
		items[i] = model.Mutation{ID: "M-111111111111", Diff: strings.Repeat("x", 512), Span: model.Span{StartByte: i}}
	}
	b.ReportAllocs()
	b.ResetTimer()
	for n := 0; n < b.N; n++ {
		s, err := New()
		if err != nil {
			b.Fatal(err)
		}
		for _, item := range items {
			if err := s.Append(item); err != nil {
				b.Fatal(err)
			}
		}
		for k := 0; k < 2; k++ {
			r, err := s.Reader()
			if err != nil {
				b.Fatal(err)
			}
			for i := 0; i < len(items); i++ {
				if _, err := r.Next(); err != nil {
					b.Fatal(err)
				}
			}
			if _, err := r.Next(); err != io.EOF {
				b.Fatalf("tail: %v", err)
			}
			if err := r.Close(); err != nil {
				b.Fatal(err)
			}
		}
		if err := s.Close(); err != nil {
			b.Fatal(err)
		}
	}
}
