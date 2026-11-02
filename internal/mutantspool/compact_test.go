package mutantspool

import (
	"encoding/binary"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/example/mutation-judge/internal/model"
)

func compactFixture(i int) model.Mutation {
	return model.Mutation{
		ID: "M-abcdef123456", Operator: "relational", RuleID: "r10",
		Span: model.Span{
			File: "path/日本語\"\\\t.go", StartByte: i * 43, EndByte: i*43 + 19,
			StartLine: i + 1, StartCol: 2, EndLine: i + 1, EndCol: 21,
		},
		Original: "x >= 2\n", Replacement: "x > 2\x00", Description: "mutated 🎲",
		Suggestion: "compare the é case", Diff: "@@ -1 +1 @@\n-old\n+new\n", EquivalentReason: "guarded equivalent",
	}
}

func TestR07S10CompactRoundTripAndSpill(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("TMPDIR", tmp)
	s, _ := NewCompact()
	defer s.Close()
	const total = memoryRecords + 30
	for i := 0; i < total; i++ {
		m := compactFixture(i)
		if i == 0 {
			m.Span.StartByte = -1
			m.Span.EndCol = -9223372036854775807
		}
		if err := s.Append(m); err != nil {
			t.Fatal(err)
		}
	}
	if s.file == nil {
		t.Fatal("test did not spill")
	}
	for pass := 0; pass < 2; pass++ {
		r, err := s.Reader()
		if err != nil {
			t.Fatal(err)
		}
		for i := 0; i < total; i++ {
			m, err := r.Next()
			if err != nil {
				t.Fatal(err)
			}
			want := compactFixture(i)
			if i == 0 {
				want.Span.StartByte = -1
				want.Span.EndCol = -9223372036854775807
			}
			if !reflect.DeepEqual(m, want) {
				t.Fatalf("item %d mismatch: %+v != %+v", i, m, want)
			}
		}
		if _, err := r.Next(); err != io.EOF {
			t.Fatalf("expected EOF, got %v", err)
		}
		if err := r.Close(); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(tmp)
	if err != nil || len(entries) != 0 {
		t.Fatalf("leaked temporary files: %v (%v)", entries, err)
	}
}

func TestR07S10CompactInMemoryAndLargeRecord(t *testing.T) {
	for _, tc := range []struct {
		name string
		big  bool
	}{{"memory", false}, {"oversized", true}} {
		t.Run(tc.name, func(t *testing.T) {
			s, _ := NewCompact()
			defer s.Close()
			m := compactFixture(1)
			if tc.big {
				m.Diff = strings.Repeat("a", memoryBytes+23)
			}
			if err := s.Append(m); err != nil {
				t.Fatal(err)
			}
			if (s.file != nil) != tc.big {
				t.Fatalf("unexpected spill=%v", s.file != nil)
			}
			r, err := s.Reader()
			if err != nil {
				t.Fatal(err)
			}
			got, err := r.Next()
			if err != nil || !reflect.DeepEqual(got, m) {
				t.Fatalf("roundtrip mismatch: %v", err)
			}
			r.Close()
		})
	}
}

func TestR07S10CompactMalformedRecords(t *testing.T) {
	good := encodeCompact(compactFixture(1))
	for _, tc := range []struct {
		name  string
		bytes []byte
	}{
		{"empty", nil}, {"short", good[:len(good)-1]},
		{"wrong length", append([]byte{0}, good[1:]...)},
		{"extra bytes", append(append([]byte(nil), good...), 1)},
		{"varint overflow", append([]byte{0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff}, good...)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := decodeCompact(tc.bytes); err == nil {
				t.Fatal("accepted corrupt record")
			}
		})
	}
}

func TestR07S10TruncatedSpoolFailsClosed(t *testing.T) {
	for _, compact := range []bool{false, true} {
		t.Run(map[bool]string{false: "json", true: "compact"}[compact], func(t *testing.T) {
			s, _ := New()
			if compact {
				s, _ = NewCompact()
			}
			defer s.Close()
			for i := 0; i < memoryRecords+2; i++ {
				if err := s.Append(compactFixture(i)); err != nil {
					t.Fatal(err)
				}
			}
			// Truncate the flushed file at the boundary between records, simulating
			// an interrupted/corrupted spool that advertises more records than stored.
			if err := s.writer.Flush(); err != nil {
				t.Fatal(err)
			}
			name := s.file.Name()
			contents, err := os.ReadFile(name)
			if err != nil {
				t.Fatal(err)
			}
			if compact {
				// Compact record starts with varint-encoded length.
				n, k := binary.Uvarint(contents)
				if k <= 0 {
					t.Fatal("bad record")
				}
				if err := os.Truncate(name, int64(k)+int64(n)); err != nil {
					t.Fatal(err)
				}
			} else {
				idx := strings.IndexByte(string(contents), '\n')
				if err := os.Truncate(name, int64(idx+1)); err != nil {
					t.Fatal(err)
				}
			}
			r, err := s.Reader()
			if err != nil {
				t.Fatal(err)
			}
			defer r.Close()
			if _, err := r.Next(); err != nil {
				t.Fatal(err)
			}
			if _, err := r.Next(); !errors.Is(err, io.ErrUnexpectedEOF) {
				t.Fatalf("expected truncated spool, got %v", err)
			}
		})
	}
}

func TestR07S10SpillFilesAreTemporary(t *testing.T) {
	s, _ := NewCompact()
	defer s.Close()
	for i := 0; i < memoryRecords+1; i++ {
		if err := s.Append(compactFixture(i)); err != nil {
			t.Fatal(err)
		}
	}
	path := s.file.Name()
	if filepath.Ext(path) != ".bin" {
		t.Fatalf("unexpected temp file name %s", path)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("file not removed: %v", err)
	}
}
