package resultspool

import (
	"github.com/example/mutation-judge/internal/model"
	"strings"
	"testing"
)

func BenchmarkR07S8SmallReport(b *testing.B) {
	items := make([]model.Result, 12)
	for i := range items {
		items[i] = model.Result{Verdict: model.VerdictSurvived, Output: strings.Repeat("x", 512), DurationMS: int64(i)}
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
			if err := s.ForEach(func(model.Result) error { return nil }); err != nil {
				b.Fatal(err)
			}
		}
		if err := s.Close(); err != nil {
			b.Fatal(err)
		}
	}
}
