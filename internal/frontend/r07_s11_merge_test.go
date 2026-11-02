package frontend

import (
	"context"
	"fmt"
	"io"
	"reflect"
	"testing"

	"github.com/example/mutation-judge/internal/model"
	"github.com/example/mutation-judge/internal/mutantspool"
)

func TestR07S11CompactMergePreservesOrderAndAllFields(t *testing.T) {
	var chunks []*mutantspool.Spool
	defer func() {
		for _, s := range chunks {
			_ = s.Close()
		}
	}()
	var expected []model.Mutation
	for src := 0; src < 9; src++ {
		s, _ := mutantspool.NewCompact()
		chunks = append(chunks, s)
		for j := 0; j < 78; j++ {
			m := model.Mutation{
				ID: fmt.Sprintf("M-%012x", uint64(src*78+j)), Operator: "boundary",
				RuleID: "r1", Span: model.Span{File: "p.go", StartByte: j*10 + src, EndByte: j*10 + src + 1},
				Original: "日本語>0", Replacement: "日本語>=0", Description: fmt.Sprintf("candidate-%d-%d", src, j),
			}
			if err := s.Append(m); err != nil {
				t.Fatal(err)
			}
			expected = append(expected, m)
		}
	}
	// Original relative order is globally offset then ID, not run order.
	for i := 1; i < len(expected); i++ {
		for j := i; j > 0 && mutationLess(expected[j], expected[j-1]); j-- {
			expected[j], expected[j-1] = expected[j-1], expected[j]
		}
	}
	merged, _ := mutantspool.NewCompact()
	defer merged.Close()
	if err := mergeCompactChunks(context.Background(), chunks, merged.AppendCompactRecord); err != nil {
		t.Fatal(err)
	}
	reader, err := merged.Reader()
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	for i, want := range expected {
		got, err := reader.Next()
		if err != nil || !reflect.DeepEqual(got, want) {
			t.Fatalf("record %d: got=%+v want=%+v err=%v", i, got, want, err)
		}
	}
	if _, err := reader.Next(); err != io.EOF {
		t.Fatalf("expected EOF, got %v", err)
	}
}

func TestR07S11CompactMergeCancellation(t *testing.T) {
	s, _ := mutantspool.NewCompact()
	defer s.Close()
	_ = s.Append(model.Mutation{ID: "M-000000000001"})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := mergeCompactChunks(ctx, []*mutantspool.Spool{s}, func(_ mutantspool.CompactRecord) error { return nil }); err == nil {
		t.Fatal("ignored canceled context")
	}
}
