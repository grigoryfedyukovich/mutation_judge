package mutantspool

import (
	"encoding/binary"
	"errors"
	"io"
	"os"
	"reflect"
	"strings"
	"testing"
)

func TestR07S17CheckedForwardPreservesBytesAndFields(t *testing.T) {
	t.Setenv("TMPDIR", t.TempDir())
	src, err := NewCompact()
	if err != nil {
		t.Fatal(err)
	}
	defer src.Close()
	dst, err := NewCompact()
	if err != nil {
		t.Fatal(err)
	}
	defer dst.Close()
	const total = 211 // force disk-backed readers and writers
	for i := 0; i < total; i++ {
		m := compactFixture(i)
		m.Original = strings.Repeat("value", i%23+1)
		m.Diff = strings.Repeat("diff", i%13+1)
		if err := src.Append(m); err != nil {
			t.Fatal(err)
		}
	}
	r, err := src.Reader()
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	for i := 0; i < total; i++ {
		rec, err := r.NextCheckedCompactRecord()
		if err != nil {
			t.Fatal(err)
		}
		if err := dst.AppendCheckedCompactRecord(rec); err != nil {
			t.Fatal(err)
		}
		if rec.startByte != i*43 {
			t.Fatalf("wrong sort key at %d", i)
		}
	}
	if _, err := r.NextCheckedCompactRecord(); err != io.EOF {
		t.Fatalf("expected EOF: %v", err)
	}
	rr, err := dst.Reader()
	if err != nil {
		t.Fatal(err)
	}
	defer rr.Close()
	for i := 0; i < total; i++ {
		got, err := rr.Next()
		if err != nil {
			t.Fatal(err)
		}
		want := compactFixture(i)
		want.Original = strings.Repeat("value", i%23+1)
		want.Diff = strings.Repeat("diff", i%13+1)
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("mutation %d mismatch", i)
		}
	}
	if _, err := rr.Next(); err != io.EOF {
		t.Fatalf("expected EOF, got %v", err)
	}
	// Verify the private wire representation itself is unchanged.
	flush := func(s *Spool) []byte {
		t.Helper()
		if err := s.writer.Flush(); err != nil {
			t.Fatal(err)
		}
		data, err := os.ReadFile(s.file.Name())
		if err != nil {
			t.Fatal(err)
		}
		return data
	}
	if !reflect.DeepEqual(flush(src), flush(dst)) {
		t.Fatal("validated forwarding changed compact wire bytes")
	}
}

func TestR07S17CheckedRecordCannotBeForgedOrReused(t *testing.T) {
	src, _ := NewCompact()
	defer src.Close()
	dst, _ := NewCompact()
	defer dst.Close()
	for i := 0; i < 2; i++ {
		if err := src.Append(compactFixture(i)); err != nil {
			t.Fatal(err)
		}
	}
	if err := dst.AppendCheckedCompactRecord(CheckedCompactRecord{}); err == nil {
		t.Fatal("zero-value checked record accepted")
	}
	r, err := src.Reader()
	if err != nil {
		t.Fatal(err)
	}
	first, err := r.NextCheckedCompactRecord()
	if err != nil {
		t.Fatal(err)
	}
	if err := dst.AppendCheckedCompactRecord(first); err != nil {
		t.Fatal(err)
	}
	if _, err := r.NextCheckedCompactRecord(); err != nil {
		t.Fatal(err)
	}
	if err := dst.AppendCheckedCompactRecord(first); err == nil {
		t.Fatal("stale borrowed buffer accepted")
	}
	if _, err := first.Mutation(); err == nil {
		t.Fatal("stale borrowed mutation decoded")
	}
	r.Close()
	if err := dst.AppendCheckedCompactRecord(first); err == nil {
		t.Fatal("record from closed reader accepted")
	}
	if dst.Len() != 1 {
		t.Fatalf("unexpected changes to destination: %d", dst.Len())
	}
}

func TestR07S17UntrustedCompactAppendStillFailsClosed(t *testing.T) {
	s, _ := NewCompact()
	defer s.Close()
	m := compactFixture(2)
	valid := encodeCompact(m)
	_, width := binary.Uvarint(valid)
	for _, bad := range [][]byte{nil, valid[width : len(valid)-1], append(append([]byte{}, valid[width:]...), 1), {0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff}} {
		if err := s.AppendCompactRecord(CompactRecord{Payload: bad}); err == nil {
			t.Fatal("accepted malformed compact payload")
		}
	}
	if s.Len() != 0 {
		t.Fatal("bad record changed spool count")
	}
	// Even a damaged on-disk input cannot produce a checked record.
	input, _ := NewCompact()
	defer input.Close()
	for i := 0; i <= memoryRecords; i++ {
		if err := input.Append(compactFixture(i)); err != nil {
			t.Fatal(err)
		}
	}
	if err := input.writer.Flush(); err != nil {
		t.Fatal(err)
	}
	f, err := os.OpenFile(input.file.Name(), os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteAt([]byte{0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff}, 0); err != nil {
		t.Fatal(err)
	}
	f.Close()
	r, err := input.Reader()
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	if _, err := r.NextCheckedCompactRecord(); err == nil || errors.Is(err, io.EOF) {
		t.Fatalf("corrupt payload accepted: %v", err)
	}
}

func BenchmarkR07S17IntermediateCompactForward(b *testing.B) {
	const total = 4096
	source, _ := NewCompact()
	defer source.Close()
	for i := 0; i < total; i++ {
		m := compactFixture(i)
		m.Description = strings.Repeat("description", 6)
		m.Diff = strings.Repeat("context", 12)
		if err := source.Append(m); err != nil {
			b.Fatal(err)
		}
	}
	for _, mode := range []string{"double-validation", "checked-once"} {
		b.Run(mode, func(b *testing.B) {
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				dst, _ := NewCompact()
				r, err := source.Reader()
				if err != nil {
					b.Fatal(err)
				}
				for j := 0; j < total; j++ {
					if mode == "double-validation" {
						rec, err := r.NextCompactRecord()
						if err != nil {
							b.Fatal(err)
						}
						if err := dst.AppendCompactRecord(rec); err != nil {
							b.Fatal(err)
						}
					} else {
						rec, err := r.NextCheckedCompactRecord()
						if err != nil {
							b.Fatal(err)
						}
						if err := dst.AppendCheckedCompactRecord(rec); err != nil {
							b.Fatal(err)
						}
					}
				}
				if err := r.Close(); err != nil {
					b.Fatal(err)
				}
				if err := dst.Close(); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
