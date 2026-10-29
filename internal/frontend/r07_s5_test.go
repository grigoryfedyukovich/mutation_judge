package frontend

import (
	"fmt"
	"reflect"
	"testing"

	"github.com/example/mutation-judge/internal/model"
)

// A single very large source file used to cause the bounded discoverer to
// allocate the whole candidate slice even with --max-mutants=1. Multiple
// enabled operators also ensure visitation order and source order differ.
func TestR07S5LargeSingleFileBoundedMatchesUnlimited(t *testing.T) {
	root := t.TempDir()
	writeBoundedSource(t, root, "huge.go", boundedSource(1200))
	opts := Options{Operators: map[string]bool{"boundary": true, "boolean": true, "relational": true}}
	full, err := Discover(root, []string{"huge.go"}, opts)
	if err != nil {
		t.Fatal(err)
	}
	if len(full) < 2400 {
		t.Fatalf("expected many mutants, got %d", len(full))
	}
	for _, n := range []int{1, 2, 3, 16, 200, len(full) - 1, len(full), len(full) + 1} {
		got, total, err := DiscoverLimited(root, []string{"huge.go"}, opts, n)
		if err != nil {
			t.Fatalf("limit %d: %v", n, err)
		}
		want := n
		if want > len(full) {
			want = len(full)
		}
		if total != len(full) || !reflect.DeepEqual(got, full[:want]) {
			t.Fatalf("limit %d: count=%d want %d; output equivalent=%t", n, total, len(full), reflect.DeepEqual(got, full[:want]))
		}
	}
}

func TestR07S5HeapRetainsEarlierCandidatesArrivingLast(t *testing.T) {
	var selectK boundedSelection
	selectK.limit = 2
	for _, offset := range []int{90, 10, 70, 50, 30, 5, 100, 20} {
		selectK.offer(model.Mutation{ID: fmt.Sprintf("M-%012x", offset), Span: model.Span{StartByte: offset}})
	}
	got := selectK.sorted()
	if len(got) != 2 || got[0].Span.StartByte != 5 || got[1].Span.StartByte != 10 {
		t.Fatalf("unexpected earliest two: %+v", got)
	}
	selectTie := boundedSelection{limit: 1}
	selectTie.offer(model.Mutation{ID: "M-ffffffffffff", Span: model.Span{StartByte: 1}})
	selectTie.offer(model.Mutation{ID: "M-000000000001", Span: model.Span{StartByte: 1}})
	if got := selectTie.sorted(); len(got) != 1 || got[0].ID != "M-000000000001" {
		t.Fatalf("ID tie-break incorrect: %+v", got)
	}
}

func TestR07S5DiscardedFilesStillValidated(t *testing.T) {
	root := t.TempDir()
	writeBoundedSource(t, root, "a.go", boundedSource(2))
	writeBoundedSource(t, root, "b.go", "package p\nfunc invalid(")
	if _, _, err := DiscoverLimited(root, []string{"a.go", "b.go"}, Options{Operators: map[string]bool{"boundary": true}}, 1); err == nil {
		t.Fatal("expected parse error in a file visited after the limit was filled")
	}
}

func BenchmarkR07S5SingleFile(b *testing.B) {
	root := b.TempDir()
	writeBoundedSource(b, root, "single.go", boundedSource(2000))
	opts := Options{Operators: map[string]bool{"boundary": true, "boolean": true, "relational": true}}
	for _, limit := range []int{0, 20} {
		name := fmt.Sprintf("limit%d", limit)
		b.Run(name, func(b *testing.B) {
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				ms, n, err := DiscoverLimited(root, []string{"single.go"}, opts, limit)
				if err != nil || len(ms) == 0 || n < 2000 {
					b.Fatalf("discover %s: %v, total=%d, retained=%d", name, err, n, len(ms))
				}
			}
		})
	}
}
