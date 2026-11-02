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

func TestR07S11RawCompactForwardAndBorrowedBuffers(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("TMPDIR", tmp)
	from, _ := NewCompact()
	defer from.Close()
	to, _ := NewCompact()
	defer to.Close()
	const n = 190 // force both readers to use file-backed buffers
	for i := 0; i < n; i++ {
		m := compactFixture(i)
		m.ID = "M-" + strings.Repeat("0", 8) + "1234"
		m.Original = strings.Repeat("original", i%11+1)
		if err := from.Append(m); err != nil {
			t.Fatal(err)
		}
	}
	r, err := from.Reader()
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < n; i++ {
		record, err := r.NextCompactRecord()
		if err != nil {
			t.Fatal(err)
		}
		if record.StartByte != i*43 || string(record.ID) != "M-000000001234" {
			t.Fatalf("incorrect compact key for %d: %+v", i, record)
		}
		if err := to.AppendCompactRecord(record); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := r.NextCompactRecord(); err != io.EOF {
		t.Fatalf("expected EOF, got %v", err)
	}
	_ = r.Close()
	reader, err := to.Reader()
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	for i := 0; i < n; i++ {
		got, err := reader.Next()
		if err != nil {
			t.Fatal(err)
		}
		want := compactFixture(i)
		want.ID = "M-000000001234"
		want.Original = strings.Repeat("original", i%11+1)
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("forwarded mutation %d corrupted: %+v", i, got)
		}
	}
	_ = reader.Close()
	_ = from.Close()
	_ = to.Close()
	entries, err := os.ReadDir(tmp)
	if err != nil || len(entries) != 0 {
		t.Fatalf("temporary runs leaked: %v: %v", entries, err)
	}
}

func TestR07S11CompactRecordMalformedFailsClosed(t *testing.T) {
	m := compactFixture(3)
	encoded := encodeCompact(m)
	_, prefix := binary.Uvarint(encoded)
	valid, err := compactKey(encoded[prefix:])
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := valid.Mutation()
	if err != nil || !reflect.DeepEqual(decoded, m) {
		t.Fatalf("decoded mutation mismatch: %+v %v", decoded, err)
	}
	for _, record := range [][]byte{
		nil,
		encoded[prefix : len(encoded)-1],
		append(append([]byte{}, encoded[prefix:]...), 1),
		{0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff},
	} {
		spool, _ := NewCompact()
		if err := spool.AppendCompactRecord(CompactRecord{Payload: record}); err == nil {
			t.Fatal("accepted malformed raw compact record")
		}
		if spool.Len() != 0 {
			t.Fatal("malformed append changed the count")
		}
		_ = spool.Close()
	}
}

func TestR07S11RawCompactCorruptionAndTrailingRecord(t *testing.T) {
	s, _ := NewCompact()
	defer s.Close()
	for i := 0; i <= memoryRecords; i++ {
		if err := s.Append(compactFixture(i)); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.writer.Flush(); err != nil {
		t.Fatal(err)
	}
	f, err := os.OpenFile(s.file.Name(), os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.Write([]byte{1}); err != nil {
		t.Fatal(err)
	}
	_ = f.Close()
	r, err := s.Reader()
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i <= memoryRecords; i++ {
		if _, err := r.NextCompactRecord(); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := r.NextCompactRecord(); err == nil || errors.Is(err, io.EOF) {
		t.Fatalf("extra bytes not detected: %v", err)
	}
	_ = r.Close()
}
