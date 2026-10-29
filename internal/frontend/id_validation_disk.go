package frontend

import (
	"bufio"
	"container/heap"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
)

const idSortChunk = 8192

// diskIDs records compact (48-bit) mutation IDs without growing a per-ID
// heap map. Duplicate checking runs after discovery using bounded external
// sorting, including across chunk/merge boundaries.
type diskIDs struct {
	file *os.File
	out  *bufio.Writer
}

func newDiskIDs() (*diskIDs, error) {
	f, err := os.CreateTemp("", "mutation-judge-ids-*.bin")
	if err != nil {
		return nil, err
	}
	return &diskIDs{file: f, out: bufio.NewWriterSize(f, 64*1024)}, nil
}

func (d *diskIDs) Add(id uint64) error {
	if d == nil || d.out == nil {
		return errors.New("closed ID file")
	}
	return binary.Write(d.out, binary.LittleEndian, id)
}

func (d *diskIDs) Close() {
	if d == nil || d.file == nil {
		return
	}
	name := d.file.Name()
	_ = d.file.Close()
	_ = os.Remove(name)
	d.file = nil
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
		if err = binary.Write(writer, binary.LittleEndian, id); err != nil {
			return "", err
		}
	}
	if err = writer.Flush(); err != nil {
		return "", err
	}
	return name, nil
}

type idMergeItem struct {
	id    uint64
	index int
}
type idMergeHeap []idMergeItem

func (h idMergeHeap) Len() int           { return len(h) }
func (h idMergeHeap) Less(i, j int) bool { return h[i].id < h[j].id }
func (h idMergeHeap) Swap(i, j int)      { h[i], h[j] = h[j], h[i] }
func (h *idMergeHeap) Push(x any)        { *h = append(*h, x.(idMergeItem)) }
func (h *idMergeHeap) Pop() any          { n := len(*h); it := (*h)[n-1]; *h = (*h)[:n-1]; return it }

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
		var id uint64
		err := binary.Read(readers[index], binary.LittleEndian, &id)
		return id, err
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
		heap.Push(&h, idMergeItem{id, i})
	}
	processed := 0
	for len(h) > 0 {
		if processed&1023 == 0 {
			if err := ctx.Err(); err != nil {
				return err
			}
		}
		item := heap.Pop(&h).(idMergeItem)
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
		heap.Push(&h, idMergeItem{id, item.index})
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
	err = mergeIDRuns(ctx, paths, func(id uint64) error { return binary.Write(w, binary.LittleEndian, id) })
	if err != nil {
		return "", err
	}
	if err = w.Flush(); err != nil {
		return "", err
	}
	return name, nil
}

// Validate closes the input stream and checks all IDs for duplicates.
// At most idSortChunk keys and discoveryMergeFanIn open inputs are needed.
func (d *diskIDs) Validate(ctx context.Context) error {
	if d == nil || d.file == nil {
		return errors.New("closed ID file")
	}
	if err := d.out.Flush(); err != nil {
		return fmt.Errorf("flush mutation IDs: %w", err)
	}
	if _, err := d.file.Seek(0, io.SeekStart); err != nil {
		return err
	}
	var paths []string
	cleanup := func() {
		for _, path := range paths {
			_ = os.Remove(path)
		}
	}
	defer cleanup()
	reader := bufio.NewReaderSize(d.file, 64*1024)
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		values := make([]uint64, 0, idSortChunk)
		for len(values) < idSortChunk {
			var id uint64
			err := binary.Read(reader, binary.LittleEndian, &id)
			if err == io.EOF {
				break
			}
			if err != nil {
				return fmt.Errorf("read ID spool: %w", err)
			}
			values = append(values, id)
		}
		if len(values) == 0 {
			break
		}
		sort.Slice(values, func(i, j int) bool { return values[i] < values[j] })
		for i := 1; i < len(values); i++ {
			if values[i] == values[i-1] {
				return fmt.Errorf("duplicate mutation ID M-%012x", values[i])
			}
		}
		name, err := createSortedIDRun(values)
		if err != nil {
			return err
		}
		paths = append(paths, name)
	}
	for len(paths) > discoveryMergeFanIn {
		next := make([]string, 0, (len(paths)+discoveryMergeFanIn-1)/discoveryMergeFanIn)
		for i := 0; i < len(paths); i += discoveryMergeFanIn {
			end := i + discoveryMergeFanIn
			if end > len(paths) {
				end = len(paths)
			}
			name, err := mergeIDRunToFile(ctx, paths[i:end])
			if err != nil {
				for _, p := range next {
					_ = os.Remove(p)
				}
				return err
			}
			next = append(next, name)
		}
		for _, p := range paths {
			_ = os.Remove(p)
		}
		paths = next
	}
	var last uint64
	haveLast := false
	return mergeIDRuns(ctx, paths, func(id uint64) error {
		if haveLast && id == last {
			return fmt.Errorf("duplicate mutation ID M-%012x", id)
		}
		last = id
		haveLast = true
		return nil
	})
}
