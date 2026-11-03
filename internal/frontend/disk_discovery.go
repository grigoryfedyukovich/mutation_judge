package frontend

import (
	"bytes"
	"context"
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
// many mutation sites. The merge fan-in bounds concurrently opened readers.
// Retaining a small multiple of the fan-in lets the scheduler avoid premature
// full-run rewrites, particularly when the final merge needs <= 16 readers.
const discoveryChunkSize = 512
const discoveryMergeFanIn = 16
const discoveryMaxPendingRuns = 3 * discoveryMergeFanIn

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
	ids, idErr := newDiskIDs(ctx)
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
			chunk, err := mutantspool.NewCompact()
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
		// Reduce to at most one final fan-in. Unlike eager base-16 carrying,
		// this does not rewrite records that can go straight to final output.
		chunks, e := runs.finish(ctx)
		if e != nil {
			runs.close()
			return nil, 0, e
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

// A merge entry contains a borrowed compact record and its originating reader.
// It avoids decoding/encoding all fields on intermediate merge passes.
type compactMergeEntry struct {
	record mutantspool.CompactRecord
	source int
}
type compactMergeHeap []compactMergeEntry

func compactLess(a, b mutantspool.CompactRecord) bool {
	if a.StartByte != b.StartByte {
		return a.StartByte < b.StartByte
	}
	return bytes.Compare(a.ID, b.ID) < 0
}
func (h *compactMergeHeap) push(v compactMergeEntry) {
	*h = append(*h, v)
	i := len(*h) - 1
	for i > 0 {
		p := (i - 1) / 2
		if !compactLess(v.record, (*h)[p].record) {
			break
		}
		(*h)[i] = (*h)[p]
		i = p
	}
	(*h)[i] = v
}
func (h *compactMergeHeap) pop() compactMergeEntry {
	first := (*h)[0]
	n := len(*h)
	last := (*h)[n-1]
	(*h)[n-1] = compactMergeEntry{}
	*h = (*h)[:n-1]
	if n == 1 {
		return first
	}
	i := 0
	for {
		child := 2*i + 1
		if child >= n-1 {
			break
		}
		if child+1 < n-1 && compactLess((*h)[child+1].record, (*h)[child].record) {
			child++
		}
		if !compactLess((*h)[child].record, last.record) {
			break
		}
		(*h)[i] = (*h)[child]
		i = child
	}
	(*h)[i] = last
	return first
}

func mergeCompactChunks(ctx context.Context, chunks []*mutantspool.Spool, emit func(mutantspool.CompactRecord) error) error {
	readers := make([]*mutantspool.Reader, len(chunks))
	defer func() {
		for _, r := range readers {
			if r != nil {
				_ = r.Close()
			}
		}
	}()
	var h compactMergeHeap
	for i, ch := range chunks {
		r, err := ch.Reader()
		if err != nil {
			return err
		}
		readers[i] = r
		rec, err := r.NextCompactRecord()
		if err == io.EOF {
			continue
		}
		if err != nil {
			return err
		}
		h.push(compactMergeEntry{rec, i})
	}
	emitted := 0
	for len(h) > 0 {
		if emitted&255 == 0 {
			if err := ctx.Err(); err != nil {
				return err
			}
		}
		item := h.pop()
		if err := emit(item.record); err != nil {
			return err
		}
		emitted++
		// Only advance the source reader after the borrowed record has been
		// consumed. Advancing sooner would overwrite its reusable buffer.
		rec, err := readers[item.source].NextCompactRecord()
		if err == io.EOF {
			continue
		}
		if err != nil {
			return err
		}
		h.push(compactMergeEntry{rec, item.source})
	}
	return nil
}

func mergeChunks(ctx context.Context, chunks []*mutantspool.Spool, emit func(model.Mutation) error) error {
	return mergeCompactChunks(ctx, chunks, func(rec mutantspool.CompactRecord) error {
		m, err := rec.Mutation()
		if err != nil {
			return err
		}
		return emit(m)
	})
}

// discoveryRuns retains only a bounded window of sorted runs. A size-aware
// partial external merge prefers the smallest runs and merges no more runs
// than necessary to stay within the final 16-reader fan-in. This avoids the
// extra full merge pass that eager base-16 carrying performed for exactly
// 16, 256, ... input runs. Only at most discoveryMaxPendingRuns descriptors
// are retained, and merges open at most discoveryMergeFanIn readers.
type discoveryRuns struct {
	pending []*mutantspool.Spool
	// These counts are useful for regression tests and profiling of the
	// private merge schedule; they are not stored in public reports.
	intermediateRecords int
	mergeCalls          int
	maxPending          int
}

// mergeSmallest combines k smallest runs; all run ownership remains with d
// on error so d.close can safely release it. On success d owns the merged
// run and the inputs are closed (including their temporary files).
func (d *discoveryRuns) mergeSmallest(ctx context.Context, k int) error {
	if k < 2 || k > discoveryMergeFanIn || k > len(d.pending) {
		return fmt.Errorf("invalid discovery merge width %d for %d runs", k, len(d.pending))
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	// Stable order breaks equal-size ties reproducibly. The final sort key
	// (source offset, mutation ID) does not depend on run merge order.
	sort.SliceStable(d.pending, func(i, j int) bool {
		return d.pending[i].Len() < d.pending[j].Len()
	})
	inputs := d.pending[:k]
	merged, err := mutantspool.NewCompact()
	if err != nil {
		return err
	}
	if err = mergeCompactChunks(ctx, inputs, merged.AppendCompactRecord); err != nil {
		_ = merged.Close()
		return err
	}
	// Mutate ownership only after the complete merge succeeds.
	remaining := append([]*mutantspool.Spool(nil), d.pending[k:]...)
	d.pending = append(remaining, merged)
	d.intermediateRecords += merged.Len()
	d.mergeCalls++
	for _, input := range inputs {
		_ = input.Close()
	}
	return nil
}

func (d *discoveryRuns) add(ctx context.Context, chunk *mutantspool.Spool) error {
	d.pending = append(d.pending, chunk)
	if len(d.pending) > d.maxPending {
		d.maxPending = len(d.pending)
	}
	// Collapse the smallest fan-in when the pending window fills, leaving
	// space for new small chunks. No final output ever opens >16 readers.
	if len(d.pending) >= discoveryMaxPendingRuns {
		return d.mergeSmallest(ctx, discoveryMergeFanIn)
	}
	return nil
}

func (d *discoveryRuns) finish(ctx context.Context) ([]*mutantspool.Spool, error) {
	for len(d.pending) > discoveryMergeFanIn {
		// Reduce to exactly the final fan-in when only a short tail remains.
		// E.g. 17 runs merge two, not an entire 16-run group.
		width := len(d.pending) - discoveryMergeFanIn + 1
		if width > discoveryMergeFanIn {
			width = discoveryMergeFanIn
		}
		if err := d.mergeSmallest(ctx, width); err != nil {
			return nil, err
		}
	}
	out := d.pending
	d.pending = nil
	return out, nil
}

func (d *discoveryRuns) close() {
	for _, ch := range d.pending {
		_ = ch.Close()
	}
	d.pending = nil
}
