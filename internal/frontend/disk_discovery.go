package frontend

import (
	"container/heap"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/example/mutation-judge/internal/model"
	"github.com/example/mutation-judge/internal/mutantspool"
)

// discoveryChunkSize bounds sorting memory when a single source file contains
// many mutation sites. Merge fan-in is also bounded to limit open descriptors.
const discoveryChunkSize = 512
const discoveryMergeFanIn = 16

// DiscoverToDisk visits every eligible file and writes candidates in the same
// file/offset/ID order as Discover. Mutants are sorted in bounded-size runs,
// merged externally, and their patches are generated only as they are emitted.
// The returned spool is owned by the caller, including on later cancellation.
func DiscoverToDisk(ctx context.Context, root string, files []string, opts Options) (out *mutantspool.Spool, count int, err error) {
	out, err = mutantspool.New()
	if err != nil {
		return nil, 0, err
	}
	owned := out
	defer func() {
		if err != nil {
			_ = owned.Close()
			out = nil
		}
	}()
	opts.root = root
	opts.pkgs = newPkgCache(root)
	opts.deferDiff = true
	ordered := append([]string(nil), files...)
	sort.Strings(ordered)
	// Write IDs to disk and validate them with an external sort after
	// discovery, avoiding a heap map proportional to the mutant count.
	ids, idErr := newDiskIDs()
	if idErr != nil {
		return nil, 0, idErr
	}
	defer ids.Close()
	for _, rel := range ordered {
		if err = ctx.Err(); err != nil {
			return nil, 0, err
		}
		var src []byte
		src, err = os.ReadFile(filepath.Join(root, filepath.FromSlash(rel)))
		if err != nil {
			return nil, 0, err
		}
		if !opts.IncludeGenerated && isGenerated(src) {
			continue
		}
		var runs discoveryRuns
		cleanup := runs.close
		var candidates []model.Mutation
		var visitErr error
		flush := func() error {
			if len(candidates) == 0 {
				return nil
			}
			sort.Slice(candidates, func(i, j int) bool { return mutationLess(candidates[i], candidates[j]) })
			chunk, err := mutantspool.New()
			if err != nil {
				return err
			}
			for _, m := range candidates {
				if err := chunk.Append(m); err != nil {
					_ = chunk.Close()
					return err
				}
			}
			if err := runs.add(ctx, chunk); err != nil {
				return err
			}
			// Release references to large strings before visiting the next chunk.
			clear(candidates)
			candidates = candidates[:0]
			return nil
		}
		err = discoverFileEmit(rel, src, opts, func(m model.Mutation) {
			if visitErr != nil {
				return
			}
			if count&255 == 0 {
				if visitErr = ctx.Err(); visitErr != nil {
					return
				}
			}
			if len(m.ID) != 14 || !strings.HasPrefix(m.ID, "M-") {
				visitErr = fmt.Errorf("malformed mutation ID %q", m.ID)
				return
			}
			id, parseErr := strconv.ParseUint(m.ID[2:], 16, 48)
			if parseErr != nil {
				visitErr = fmt.Errorf("invalid mutation ID %q: %w", m.ID, parseErr)
				return
			}
			if visitErr = ids.Add(id); visitErr != nil {
				return
			}
			count++
			candidates = append(candidates, m)
			if len(candidates) == discoveryChunkSize {
				visitErr = flush()
			}
		})
		if err == nil {
			err = visitErr
		}
		if err == nil {
			err = flush()
		}
		if err != nil {
			cleanup()
			return nil, 0, err
		}
		// Bounded fan-in multi-pass merge: never open one reader per chunk in
		// giant source files. Pending-run metadata is O(log candidates).
		chunks := runs.takeAll()
		for len(chunks) > discoveryMergeFanIn {
			var next []*mutantspool.Spool
			for i := 0; i < len(chunks); i += discoveryMergeFanIn {
				end := i + discoveryMergeFanIn
				if end > len(chunks) {
					end = len(chunks)
				}
				merged, e := mutantspool.New()
				if e == nil {
					e = mergeChunks(ctx, chunks[i:end], merged.Append)
				}
				if e != nil {
					if merged != nil {
						_ = merged.Close()
					}
					for _, ch := range next {
						_ = ch.Close()
					}
					for _, ch := range chunks {
						_ = ch.Close()
					}
					return nil, 0, e
				}
				next = append(next, merged)
			}
			for _, ch := range chunks {
				_ = ch.Close()
			}
			chunks = next
		}
		lines := newLineIndex(src)
		err = mergeChunks(ctx, chunks, func(m model.Mutation) error {
			m.Diff = unifiedDiff(rel, src, lines, m.Span.StartByte, m.Span.EndByte, m.Replacement)
			return out.Append(m)
		})
		for _, ch := range chunks {
			_ = ch.Close()
		}
		if err != nil {
			return nil, 0, err
		}
	}
	if err = ids.Validate(ctx); err != nil {
		return nil, 0, err
	}
	return out, count, nil
}

type mergeEntry struct {
	mutant model.Mutation
	source int
}
type mergeHeap []mergeEntry

func (h mergeHeap) Len() int           { return len(h) }
func (h mergeHeap) Less(i, j int) bool { return mutationLess(h[i].mutant, h[j].mutant) }
func (h mergeHeap) Swap(i, j int)      { h[i], h[j] = h[j], h[i] }
func (h *mergeHeap) Push(x any)        { *h = append(*h, x.(mergeEntry)) }
func (h *mergeHeap) Pop() any {
	n := len(*h)
	v := (*h)[n-1]
	(*h)[n-1] = mergeEntry{}
	*h = (*h)[:n-1]
	return v
}
func mergeChunks(ctx context.Context, chunks []*mutantspool.Spool, emit func(model.Mutation) error) error {
	readers := make([]*mutantspool.Reader, len(chunks))
	defer func() {
		for _, r := range readers {
			if r != nil {
				_ = r.Close()
			}
		}
	}()
	var h mergeHeap
	for i, ch := range chunks {
		r, err := ch.Reader()
		if err != nil {
			return err
		}
		readers[i] = r
		m, err := r.Next()
		if err == io.EOF {
			continue
		}
		if err != nil {
			return err
		}
		heap.Push(&h, mergeEntry{m, i})
	}
	emitted := 0
	for len(h) > 0 {
		if emitted&255 == 0 {
			if err := ctx.Err(); err != nil {
				return err
			}
		}
		item := heap.Pop(&h).(mergeEntry)
		if err := emit(item.mutant); err != nil {
			return err
		}
		emitted++
		m, err := readers[item.source].Next()
		if errors.Is(err, io.EOF) {
			continue
		}
		if err != nil {
			return err
		}
		heap.Push(&h, mergeEntry{m, item.source})
	}
	return nil
}

// discoveryRuns uses a base-16 carry structure. Every new sorted chunk is
// merged into higher levels as needed, limiting retained descriptors and
// chunk metadata to O(log(number of candidates)) rather than O(candidates).
type discoveryRuns struct{ levels [][]*mutantspool.Spool }

func (d *discoveryRuns) add(ctx context.Context, chunk *mutantspool.Spool) error {
	for level := 0; ; level++ {
		for len(d.levels) <= level {
			d.levels = append(d.levels, nil)
		}
		d.levels[level] = append(d.levels[level], chunk)
		if len(d.levels[level]) < discoveryMergeFanIn {
			return nil
		}
		merged, err := mutantspool.New()
		if err == nil {
			err = mergeChunks(ctx, d.levels[level], merged.Append)
		}
		if err != nil {
			if merged != nil {
				_ = merged.Close()
			}
			return err
		}
		for _, old := range d.levels[level] {
			_ = old.Close()
		}
		d.levels[level] = nil
		chunk = merged
	}
}
func (d *discoveryRuns) close() {
	for _, level := range d.levels {
		for _, ch := range level {
			_ = ch.Close()
		}
	}
	d.levels = nil
}
func (d *discoveryRuns) takeAll() []*mutantspool.Spool {
	var out []*mutantspool.Spool
	for _, level := range d.levels {
		out = append(out, level...)
	}
	d.levels = nil
	return out
}
