package frontend

import (
	"context"
	"fmt"
	"io"
	"os"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/example/mutation-judge/internal/model"
	"github.com/example/mutation-judge/internal/mutantspool"
)

func makeR07S12Run(t *testing.T, first, count int) *mutantspool.Spool {
	t.Helper()
	s, err := mutantspool.NewCompact()
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < count; i++ {
		v := first + i
		m := model.Mutation{
			ID:          fmt.Sprintf("M-%012x", v),
			Operator:    "boundary",
			Span:        model.Span{File: "test.go", StartByte: v * 2, EndByte: v*2 + 1},
			Original:    ">",
			Replacement: ">=",
		}
		if err := s.Append(m); err != nil {
			_ = s.Close()
			t.Fatal(err)
		}
	}
	return s
}

func TestR07S12DefersUnnecessaryMergePasses(t *testing.T) {
	for _, tc := range []struct {
		name               string
		runs, intermediate int
		merges             int
	}{
		{"one", 1, 0, 0},
		{"exact-fan-in", discoveryMergeFanIn, 0, 0},
		{"one-over-fan-in", discoveryMergeFanIn + 1, 2, 1},
		{"two-over-fan-in", discoveryMergeFanIn + 2, 3, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("TMPDIR", t.TempDir())
			var d discoveryRuns
			defer d.close()
			for i := 0; i < tc.runs; i++ {
				if err := d.add(context.Background(), makeR07S12Run(t, i, 1)); err != nil {
					t.Fatal(err)
				}
			}
			final, err := d.finish(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			defer func() {
				for _, s := range final {
					_ = s.Close()
				}
			}()
			if d.intermediateRecords != tc.intermediate || d.mergeCalls != tc.merges || len(final) > discoveryMergeFanIn {
				t.Fatalf("runs=%d: intermediate=%d merges=%d final runs=%d", tc.runs, d.intermediateRecords, d.mergeCalls, len(final))
			}
			var ids []string
			err = mergeChunks(context.Background(), final, func(m model.Mutation) error {
				ids = append(ids, m.ID)
				return nil
			})
			if err != nil {
				t.Fatal(err)
			}
			if len(ids) != tc.runs || !sort.StringsAreSorted(ids) {
				t.Fatalf("incorrect final ordering: %v", ids)
			}
		})
	}
}

func TestR07S12ManyRunsRemainBoundedAndDeterministic(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("TMPDIR", tmp)
	var d discoveryRuns
	defer d.close()
	const runs = 300
	const perRun = 72
	for i := 0; i < runs; i++ {
		// Reverse the order of the runs to ensure the final result really
		// comes from the merge key, not the order in which runs arrived.
		if err := d.add(context.Background(), makeR07S12Run(t, (runs-i-1)*perRun, perRun)); err != nil {
			t.Fatal(err)
		}
		if len(d.pending) > discoveryMaxPendingRuns {
			t.Fatalf("retained %d runs above limit %d", len(d.pending), discoveryMaxPendingRuns)
		}
	}
	final, err := d.finish(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if d.maxPending > discoveryMaxPendingRuns || len(final) > discoveryMergeFanIn {
		t.Fatalf("peak=%d final=%d", d.maxPending, len(final))
	}
	var actual []string
	err = mergeChunks(context.Background(), final, func(m model.Mutation) error {
		actual = append(actual, m.ID)
		return nil
	})
	for _, s := range final {
		_ = s.Close()
	}
	if err != nil {
		t.Fatal(err)
	}
	if len(actual) != runs*perRun || !sort.StringsAreSorted(actual) {
		t.Fatalf("output is missing or out of order (%d records)", len(actual))
	}
	for i, id := range actual {
		if want := fmt.Sprintf("M-%012x", i); id != want {
			t.Fatalf("record %d: got %q want %q", i, id, want)
		}
	}
	if entries, err := os.ReadDir(tmp); err != nil || len(entries) != 0 {
		t.Fatalf("temp files remain: %v (%v)", entries, err)
	}
}

func TestR07S12MergeErrorAndCancellationLeaveNoTempFiles(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("TMPDIR", tmp)
	var d discoveryRuns
	for i := 0; i <= discoveryMergeFanIn; i++ {
		if err := d.add(context.Background(), makeR07S12Run(t, i, 1)); err != nil {
			t.Fatal(err)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := d.finish(ctx); err == nil {
		t.Fatal("expected cancellation")
	}
	d.close()
	if entries, err := os.ReadDir(tmp); err != nil || len(entries) != 0 {
		t.Fatalf("temporary files leaked: %v (%v)", entries, err)
	}
}

func TestR07S12RawMergePreservesAllMutationFields(t *testing.T) {
	var d discoveryRuns
	defer d.close()
	var expected []model.Mutation
	for i := 0; i < discoveryMergeFanIn+1; i++ {
		run, err := mutantspool.NewCompact()
		if err != nil {
			t.Fatal(err)
		}
		for j := 0; j < 4; j++ {
			v := 4*i + j
			m := model.Mutation{ID: fmt.Sprintf("M-%012x", v), Operator: "boundary", RuleID: "r1", Description: strings.Repeat("Ω", 7), Span: model.Span{File: "a.go", StartByte: v, EndByte: v + 1}, Original: ">", Replacement: ">="}
			if err := run.Append(m); err != nil {
				t.Fatal(err)
			}
			expected = append(expected, m)
		}
		if err := d.add(context.Background(), run); err != nil {
			t.Fatal(err)
		}
	}
	final, err := d.finish(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		for _, s := range final {
			_ = s.Close()
		}
	}()
	var actual []model.Mutation
	err = mergeChunks(context.Background(), final, func(m model.Mutation) error {
		actual = append(actual, m)
		return nil
	})
	if err != nil && err != io.EOF {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(actual, expected) {
		t.Fatal("the merge changed candidate data")
	}
}
