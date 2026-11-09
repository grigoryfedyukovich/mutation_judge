package mutantspool

import (
	"bytes"
	"encoding/binary"
	"io"
	"reflect"
	"strings"
	"testing"

	"github.com/example/mutation-judge/internal/model"
)

// An independent copy of the previous compact writer protects the exact
// version-local framing, including varint widths and unusual string contents.
func legacyCompactEncoding(m model.Mutation) []byte {
	b := make([]byte, binary.MaxVarintLen64, len(m.ID)+len(m.Original)+len(m.Replacement)+len(m.Description)+len(m.Suggestion)+len(m.Diff)+len(m.EquivalentReason)+256)
	for _, s := range []string{m.ID, m.Operator, m.RuleID, m.Span.File} {
		b = binary.AppendUvarint(b, uint64(len(s)))
		b = append(b, s...)
	}
	for _, v := range []int{m.Span.StartByte, m.Span.EndByte, m.Span.StartLine, m.Span.StartCol, m.Span.EndLine, m.Span.EndCol} {
		b = binary.AppendVarint(b, int64(v))
	}
	for _, s := range []string{m.Original, m.Replacement, m.Description, m.Suggestion, m.Diff, m.EquivalentReason} {
		b = binary.AppendUvarint(b, uint64(len(s)))
		b = append(b, s...)
	}
	prefix := binary.PutUvarint(b, uint64(len(b)-binary.MaxVarintLen64))
	copy(b[prefix:], b[binary.MaxVarintLen64:])
	return b[:prefix+len(b)-binary.MaxVarintLen64]
}

func TestR07S16CompactWriterPreservesWireBytes(t *testing.T) {
	fixtures := []model.Mutation{
		{}, compactFixture(-1), compactFixture(0), compactFixture(1 << 20),
		{
			ID: strings.Repeat("界", 200), RuleID: "rule\x00newline\n",
			Span:     model.Span{File: "a/🌍/b.go", StartByte: -9223372036854775807, EndByte: 9223372036854775807},
			Original: "x\x00y", Replacement: strings.Repeat("=", 700),
			Diff: strings.Repeat("a\n", 32<<10),
		},
	}
	for i, m := range fixtures {
		want := legacyCompactEncoding(m)
		if got := encodeCompact(m); !bytes.Equal(got, want) {
			t.Fatalf("fixture %d: initial encoding changed", i)
		}
		scratch := make([]byte, 0, len(want)+256)
		for iteration := 0; iteration < 3; iteration++ {
			got := encodeCompactInto(scratch, m)
			if !bytes.Equal(got, want) {
				t.Fatalf("fixture %d, iteration %d: reusable encoding changed", i, iteration)
			}
			scratch = got[:0]
		}
	}
}

func TestR07S16CompactScratchIsNotAliasedAndIsBounded(t *testing.T) {
	for _, constructor := range []struct {
		name string
		new  func() (*Spool, error)
	}{
		{"merge", NewCompact}, {"final", NewFinalCompact},
	} {
		t.Run(constructor.name, func(t *testing.T) {
			s, err := constructor.new()
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			expected := make([]model.Mutation, 0, memoryRecords+14)
			for i := 0; i < memoryRecords+10; i++ {
				m := compactFixture(i)
				m.ID = string(rune('a'+i)) + strings.Repeat("f", i%17)
				m.Diff = strings.Repeat("line\n", i+1)
				expected = append(expected, m)
				if err := s.Append(m); err != nil {
					t.Fatal(err)
				}
			}
			if s.file == nil || len(s.compactWriteScratch) != 0 || cap(s.compactWriteScratch) == 0 {
				t.Fatalf("disk spool does not own a reusable scratch buffer")
			}
			if cap(s.compactWriteScratch) > maxCompactWriteScratch {
				t.Fatal("retained scratch exceeds bound")
			}
			// This record requires a large temporary allocation, but the
			// large allocation must not remain attached to the spool.
			large := compactFixture(memoryRecords + 10)
			large.Diff = strings.Repeat("xyz", 1<<18)
			expected = append(expected, large)
			if err := s.Append(large); err != nil {
				t.Fatal(err)
			}
			if cap(s.compactWriteScratch) != 0 {
				t.Fatalf("oversized scratch retained: cap=%d", cap(s.compactWriteScratch))
			}
			for i := memoryRecords + 11; i < memoryRecords+14; i++ {
				m := compactFixture(i)
				expected = append(expected, m)
				if err := s.Append(m); err != nil {
					t.Fatal(err)
				}
			}
			// Read twice after repeated reuse to catch aliasing of prior
			// records and ensure the appended mutations match the oracle.
			for pass := 0; pass < 2; pass++ {
				r, err := s.Reader()
				if err != nil {
					t.Fatal(err)
				}
				for i, want := range expected {
					got, err := r.Next()
					if err != nil || !reflect.DeepEqual(got, want) {
						t.Fatalf("pass %d mutation %d: %+v (%v) != %+v", pass, i, got, err, want)
					}
				}
				if _, err := r.Next(); err != io.EOF {
					t.Fatalf("pass %d expected EOF, got %v", pass, err)
				}
				if err := r.Close(); err != nil {
					t.Fatal(err)
				}
			}
		})
	}
}

func TestR07S16JSONSpoolUnchanged(t *testing.T) {
	s, err := New()
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	for i := 0; i < memoryRecords+3; i++ {
		if err := s.Append(compactFixture(i)); err != nil {
			t.Fatal(err)
		}
	}
	if s.compactWriteScratch != nil {
		t.Fatal("JSON spool allocated compact scratch")
	}
	r, err := s.Reader()
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	for i := 0; i < memoryRecords+3; i++ {
		got, err := r.Next()
		if err != nil || !reflect.DeepEqual(got, compactFixture(i)) {
			t.Fatalf("JSON mutation %d: %+v %v", i, got, err)
		}
	}
}
