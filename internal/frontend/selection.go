package frontend

import (
	"container/heap"
	"sort"

	"github.com/example/mutation-judge/internal/model"
)

// mutationLess is the historical ordering within a file. A source visitor
// cannot assume AST visitation order equals byte-offset order: for instance,
// statement mutations can be discovered ahead of nested expression mutations.
func mutationLess(a, b model.Mutation) bool {
	if a.Span.StartByte != b.Span.StartByte {
		return a.Span.StartByte < b.Span.StartByte
	}
	return a.ID < b.ID
}

// maxMutationHeap puts the *latest* mutation at the root so the root can
// be replaced when a newly discovered candidate belongs in the first K.
type maxMutationHeap []model.Mutation

func (h maxMutationHeap) Len() int           { return len(h) }
func (h maxMutationHeap) Less(i, j int) bool { return mutationLess(h[j], h[i]) }
func (h maxMutationHeap) Swap(i, j int)      { h[i], h[j] = h[j], h[i] }
func (h *maxMutationHeap) Push(x any)        { *h = append(*h, x.(model.Mutation)) }
func (h *maxMutationHeap) Pop() any {
	old := *h
	last := len(old) - 1
	m := old[last]
	old[last] = model.Mutation{}
	*h = old[:last]
	return m
}

type boundedSelection struct {
	limit int
	items maxMutationHeap
}

func (s *boundedSelection) offer(m model.Mutation) {
	if s.limit <= 0 {
		return
	}
	if len(s.items) < s.limit {
		heap.Push(&s.items, m)
		return
	}
	if mutationLess(m, s.items[0]) {
		s.items[0] = m
		heap.Fix(&s.items, 0)
	}
}

func (s *boundedSelection) sorted() []model.Mutation {
	sort.Slice(s.items, func(i, j int) bool { return mutationLess(s.items[i], s.items[j]) })
	return s.items
}
