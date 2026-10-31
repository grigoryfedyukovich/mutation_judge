package frontend

import (
	"bufio"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
)

const idSortChunk = 8192

// diskIDs keeps small discoveries in memory and turns large discoveries into
// sorted binary runs directly. No raw ID spool or second read/sort pass is
// needed. A base-16 carry merge bounds run metadata and file descriptors.
// Duplicate detection spans chunks and merge levels, including IDs that do
// not survive bounded mutation selection.
type diskIDs struct {
	ctx       context.Context
	pending   []uint64
	levels    [][]string
	closed    bool
	validated bool
}

func newDiskIDs(ctx context.Context) (*diskIDs, error) {
	if ctx == nil {
		return nil, errors.New("nil ID validation context")
	}
	return &diskIDs{ctx: ctx}, nil
}

func (d *diskIDs) Add(id uint64) error {
	if d == nil || d.closed || d.validated {
		return errors.New("closed ID validator")
	}
	if err := d.ctx.Err(); err != nil {
		return err
	}
	if len(d.pending) == idSortChunk {
		if err := d.flush(); err != nil {
			return err
		}
	}
	d.pending = append(d.pending, id)
	return nil
}

func (d *diskIDs) Close() {
	if d == nil || d.closed {
		return
	}
	for _, level := range d.levels {
		for _, path := range level {
			_ = os.Remove(path)
		}
	}
	d.levels = nil
	d.pending = nil
	d.closed = true
}

func checkSortedIDs(values []uint64) error {
	for i := 1; i < len(values); i++ {
		if values[i] == values[i-1] {
			return fmt.Errorf("duplicate mutation ID M-%012x", values[i])
		}
	}
	return nil
}

func (d *diskIDs) addSortedRun(path string) error {
	for level := 0; ; level++ {
		for len(d.levels) <= level {
			d.levels = append(d.levels, nil)
		}
		if len(d.levels[level]) < discoveryMergeFanIn-1 {
			d.levels[level] = append(d.levels[level], path)
			return nil
		}
		// When a carry is needed, merge the prior 15 files and the new
		// run. On failure the caller retains ownership of the new run.
		paths := append(append([]string(nil), d.levels[level]...), path)
		merged, err := mergeIDRunToFile(d.ctx, paths)
		if err != nil {
			// A carried run may have been created at an earlier level;
			// it no longer belongs to the pending buffer or any level.
			_ = os.Remove(path)
			return err
		}
		for _, old := range paths {
			_ = os.Remove(old)
		}
		d.levels[level] = nil
		path = merged
	}
}

func (d *diskIDs) flush() error {
	if len(d.pending) == 0 {
		return nil
	}
	if err := d.ctx.Err(); err != nil {
		return err
	}
	sort.Slice(d.pending, func(i, j int) bool { return d.pending[i] < d.pending[j] })
	if err := checkSortedIDs(d.pending); err != nil {
		return err
	}
	path, err := createSortedIDRun(d.pending)
	if err != nil {
		return err
	}
	if err := d.addSortedRun(path); err != nil {
		_ = os.Remove(path)
		return err
	}
	// Keep the allocated capacity for the next chunk instead of allocating
	// 8192 new keys for every run.
	d.pending = d.pending[:0]
	return nil
}

func createSortedIDRun(values []uint64) (string, error) {
	file, err := os.CreateTemp("", "mutation-judge-idrun-*.bin")
	if err != nil {
		return "", err
	}
	name := file.Name()
	owned := name
	defer func() {
		_ = file.Close()
		if err != nil {
			_ = os.Remove(owned)
		}
	}()
	writer := bufio.NewWriterSize(file, 64*1024)
	for _, id := range values {
		if err = writeSortedID(writer, id); err != nil {
			return "", err
		}
	}
	if err = writer.Flush(); err != nil {
		return "", err
	}
	return name, nil
}

// Use fixed-width, allocation-free binary I/O for sorted ID runs. The raw
// ID spool formerly used encoding/binary.Read/Write for each ID, which
// allocated temporary reflection buffers during every merge pass.
func writeSortedID(w *bufio.Writer, id uint64) error {
	var b [8]byte
	binary.LittleEndian.PutUint64(b[:], id)
	_, err := w.Write(b[:])
	return err
}

func readSortedID(r *bufio.Reader) (uint64, error) {
	b, err := r.Peek(8)
	if err != nil {
		if errors.Is(err, io.EOF) && len(b) > 0 {
			return 0, io.ErrUnexpectedEOF
		}
		return 0, err
	}
	id := binary.LittleEndian.Uint64(b)
	_, err = r.Discard(8)
	return id, err
}

type idMergeItem struct {
	id    uint64
	index int
}
type idMergeHeap []idMergeItem

// Specialized binary min-heap avoids interface boxing on every ID merge.
func (h *idMergeHeap) push(it idMergeItem) {
	*h = append(*h, it)
	i := len(*h) - 1
	for i > 0 {
		p := (i - 1) / 2
		if (*h)[p].id <= it.id {
			break
		}
		(*h)[i] = (*h)[p]
		i = p
	}
	(*h)[i] = it
}

func (h *idMergeHeap) pop() idMergeItem {
	n := len(*h)
	it := (*h)[0]
	last := (*h)[n-1]
	*h = (*h)[:n-1]
	if n == 1 {
		return it
	}
	i := 0
	for {
		child := 2*i + 1
		if child >= n-1 {
			break
		}
		if child+1 < n-1 && (*h)[child+1].id < (*h)[child].id {
			child++
		}
		if (*h)[child].id >= last.id {
			break
		}
		(*h)[i] = (*h)[child]
		i = child
	}
	(*h)[i] = last
	return it
}

func mergeIDRuns(ctx context.Context, paths []string, emit func(uint64) error) error {
	files := make([]*os.File, len(paths))
	readers := make([]*bufio.Reader, len(paths))
	defer func() {
		for _, f := range files {
			if f != nil {
				_ = f.Close()
			}
		}
	}()
	var h idMergeHeap
	next := func(index int) (uint64, error) {
		return readSortedID(readers[index])
	}
	for i, name := range paths {
		f, err := os.Open(name)
		if err != nil {
			return err
		}
		files[i] = f
		readers[i] = bufio.NewReaderSize(f, 64*1024)
		id, err := next(i)
		if err == io.EOF {
			continue
		}
		if err != nil {
			return err
		}
		h.push(idMergeItem{id, i})
	}
	processed := 0
	for len(h) > 0 {
		if processed&1023 == 0 {
			if err := ctx.Err(); err != nil {
				return err
			}
		}
		item := h.pop()
		if err := emit(item.id); err != nil {
			return err
		}
		processed++
		id, err := next(item.index)
		if err == io.EOF {
			continue
		}
		if err != nil {
			return err
		}
		h.push(idMergeItem{id, item.index})
	}
	return nil
}

func mergeIDRunToFile(ctx context.Context, paths []string) (name string, err error) {
	f, err := os.CreateTemp("", "mutation-judge-idrun-*.bin")
	if err != nil {
		return "", err
	}
	name = f.Name()
	owned := name
	defer func() {
		_ = f.Close()
		if err != nil {
			_ = os.Remove(owned)
		}
	}()
	w := bufio.NewWriterSize(f, 64*1024)
	err = mergeIDRuns(ctx, paths, func(id uint64) error { return writeSortedID(w, id) })
	if err != nil {
		return "", err
	}
	if err = w.Flush(); err != nil {
		return "", err
	}
	return name, nil
}

// Validate checks all IDs, including duplicates across disk runs. Discoveries
// of at most idSortChunk IDs never create a temporary ID file. Larger
// discoveries use at most idSortChunk in-memory keys and bounded fan-in.
func (d *diskIDs) Validate(ctx context.Context) error {
	if d == nil || d.closed || d.validated {
		return errors.New("closed ID validator")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := d.ctx.Err(); err != nil {
		return err
	}
	if len(d.levels) == 0 {
		sort.Slice(d.pending, func(i, j int) bool { return d.pending[i] < d.pending[j] })
		if err := checkSortedIDs(d.pending); err != nil {
			return err
		}
		d.validated = true
		return nil
	}
	if err := d.flush(); err != nil {
		return err
	}
	var last uint64
	haveLast := false
	check := func(id uint64) error {
		if haveLast && id == last {
			return fmt.Errorf("duplicate mutation ID M-%012x", id)
		}
		last, haveLast = id, true
		return nil
	}
	// Each level carries at most 15 runs, so flattening the remaining
	// runs requires O(log N) file paths, not O(N / idSortChunk).
	var paths []string
	for _, level := range d.levels {
		paths = append(paths, level...)
	}
	// Files from all levels are sorted but their ranges may overlap.
	// Bound the number of simultaneous readers even at extreme scales.
	for len(paths) > discoveryMergeFanIn {
		var next []string
		for i := 0; i < len(paths); i += discoveryMergeFanIn {
			end := i + discoveryMergeFanIn
			if end > len(paths) {
				end = len(paths)
			}
			name, err := mergeIDRunToFile(ctx, paths[i:end])
			if err != nil {
				for _, newPath := range next {
					_ = os.Remove(newPath)
				}
				return err
			}
			next = append(next, name)
		}
		for _, old := range paths {
			_ = os.Remove(old)
		}
		paths = next
		// Update the owned set after each successful merge pass, so a
		// later failure cannot orphan runs from an earlier pass.
		d.levels = [][]string{paths}
	}
	// Point cleanup ownership at the final runs, including on a validation
	// error, so Close always removes files created by the final merge.
	d.levels = [][]string{paths}
	if err := mergeIDRuns(ctx, paths, check); err != nil {
		return err
	}
	d.validated = true
	return nil
}
