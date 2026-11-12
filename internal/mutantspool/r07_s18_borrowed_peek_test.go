package mutantspool

import (
	"errors"
	"github.com/example/mutation-judge/internal/model"
	"io"
	"os"
	"reflect"
	"strings"
	"testing"
)

// Small disk-backed compact records are borrowed from bufio.Reader's backing
// buffer. Mixed-size records must still decode correctly when a refill moves
// unread bytes, and large records must fall back to the scratch-buffer path.
func TestR07S18BorrowedCompactRecordsAcrossBufferBoundaries(t *testing.T) {
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

	var want []model.Mutation
	for i := 0; i < 220; i++ {
		m := compactFixture(i)
		switch i {
		case 69:
			m.Diff = strings.Repeat("near-boundary", 4500) // fits 64 KiB
		case 70:
			m.Diff = strings.Repeat("large-record", 6500) // exceeds 64 KiB
		case 71:
			m.Diff = strings.Repeat("post-large", 150)
		default:
			m.Diff = strings.Repeat("line", i%17+1)
		}
		want = append(want, m)
		if err := src.Append(m); err != nil {
			t.Fatal(err)
		}
	}
	if src.file == nil {
		t.Fatal("fixture did not spill to disk")
	}
	r, err := src.Reader()
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	var previous CheckedCompactRecord
	for i, expected := range want {
		rec, err := r.NextCheckedCompactRecord()
		if err != nil {
			t.Fatalf("record %d: %v", i, err)
		}
		if i > 0 {
			if _, err := previous.Mutation(); err == nil {
				t.Fatalf("previous borrowed record still accepted after advancing at %d", i)
			}
		}
		if i == 0 && cap(r.rawScratch) != 0 {
			t.Fatal("small buffered record unexpectedly allocated a copy buffer")
		}
		if i == 69 && cap(r.rawScratch) != 0 {
			t.Fatal("record fitting the buffer unexpectedly allocated scratch")
		}
		if i == 70 && cap(r.rawScratch) <= r.compactReader.Size() {
			t.Fatal("oversized record failed to use scratch fallback")
		}
		got, err := rec.Mutation()
		if err != nil || !reflect.DeepEqual(got, expected) {
			t.Fatalf("record %d corrupted: %v", i, err)
		}
		if err := dst.AppendCheckedCompactRecord(rec); err != nil {
			t.Fatalf("forward %d: %v", i, err)
		}
		previous = rec
	}
	if _, err := r.NextCheckedCompactRecord(); !errors.Is(err, io.EOF) {
		t.Fatalf("EOF: %v", err)
	}
	r2, err := dst.Reader()
	if err != nil {
		t.Fatal(err)
	}
	defer r2.Close()
	for i, expected := range want {
		m, err := r2.Next()
		if err != nil || !reflect.DeepEqual(m, expected) {
			t.Fatalf("forwarded %d corrupted: %v", i, err)
		}
	}
}

func TestR07S18TruncatedBufferedRecordFailsClosed(t *testing.T) {
	t.Setenv("TMPDIR", t.TempDir())
	s, _ := NewCompact()
	defer s.Close()
	for i := 0; i < memoryRecords+8; i++ {
		if err := s.Append(compactFixture(i)); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.writer.Flush(); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(s.file.Name())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Truncate(s.file.Name(), info.Size()-3); err != nil {
		t.Fatal(err)
	}
	r, err := s.Reader()
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	for i := 0; i < s.Len()-1; i++ {
		if _, err := r.NextCheckedCompactRecord(); err != nil {
			t.Fatalf("good record %d: %v", i, err)
		}
	}
	if _, err := r.NextCheckedCompactRecord(); err == nil || errors.Is(err, io.EOF) {
		t.Fatalf("truncated buffered payload was accepted or mistaken for clean EOF: %v", err)
	}
}
