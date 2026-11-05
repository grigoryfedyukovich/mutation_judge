package mutantspool

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/example/mutation-judge/internal/model"
)

func finalFixture(i int) model.Mutation {
	return model.Mutation{
		ID: "M-123456789abc", Operator: "boundary", RuleID: "a/b",
		Span:     model.Span{File: "p/quote\"\\☃.go", StartByte: i * 10, EndByte: i*10 + 4, StartLine: i + 1, StartCol: 3, EndLine: i + 2, EndCol: 5},
		Original: "a\n\"b\\c", Replacement: "x\tλ\x00", Description: "Description\n☃", Suggestion: "Test boundary",
		Diff: "--- a/☃.go\n+++ b/☃.go\n@@ -1 +1 @@\n-a\n+b\n", EquivalentReason: "proven unreachable",
	}
}

func TestR07S14FinalCompactMatchesJSONSpool(t *testing.T) {
	temp := t.TempDir()
	t.Setenv("TMPDIR", temp)
	final, err := NewFinalCompact()
	if err != nil {
		t.Fatal(err)
	}
	defer final.Close()
	legacy, err := New()
	if err != nil {
		t.Fatal(err)
	}
	defer legacy.Close()
	const n = memoryRecords + 25
	for i := 0; i < n; i++ {
		m := finalFixture(i)
		if i%2 == 0 {
			m.EquivalentReason = ""
		}
		if err := final.Append(m); err != nil {
			t.Fatal(err)
		}
		if err := legacy.Append(m); err != nil {
			t.Fatal(err)
		}
	}
	if final.file == nil || legacy.file == nil {
		t.Fatal("expected both spools to spill")
	}
	if filepath.Ext(final.file.Name()) != ".bin" {
		t.Fatalf("final spool is not binary: %s", final.file.Name())
	}
	for pass := 0; pass < 2; pass++ {
		fr, err := final.Reader()
		if err != nil {
			t.Fatal(err)
		}
		jr, err := legacy.Reader()
		if err != nil {
			t.Fatal(err)
		}
		for i := 0; i < n; i++ {
			fm, fe := fr.Next()
			jm, je := jr.Next()
			if fe != nil || je != nil || !reflect.DeepEqual(fm, jm) || !reflect.DeepEqual(fm, func() model.Mutation {
				m := finalFixture(i)
				if i%2 == 0 {
					m.EquivalentReason = ""
				}
				return m
			}()) {
				t.Fatalf("pass %d record %d compact %+v (%v), json %+v (%v)", pass, i, fm, fe, jm, je)
			}
			// Public JSON fields and omission semantics remain unchanged.
			compactJSON, err := json.Marshal(fm)
			if err != nil {
				t.Fatal(err)
			}
			legacyJSON, err := json.Marshal(jm)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(compactJSON, legacyJSON) {
				t.Fatal("report JSON differs")
			}
		}
		if _, err := fr.Next(); err != io.EOF {
			t.Fatalf("compact tail: %v", err)
		}
		if _, err := jr.Next(); err != io.EOF {
			t.Fatalf("JSON tail: %v", err)
		}
		if err := fr.Close(); err != nil {
			t.Fatal(err)
		}
		if err := jr.Close(); err != nil {
			t.Fatal(err)
		}
	}
	if err := final.Close(); err != nil {
		t.Fatal(err)
	}
	if err := legacy.Close(); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(temp)
	if err != nil || len(entries) != 0 {
		t.Fatalf("temp files %v: %v", entries, err)
	}
}

func TestR07S14SmallFinalSpoolNeedsNoDisk(t *testing.T) {
	t.Setenv("TMPDIR", filepath.Join(t.TempDir(), "nonexistent"))
	s, err := NewFinalCompact()
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	for i := 0; i < 10; i++ {
		if err := s.Append(finalFixture(i)); err != nil {
			t.Fatal(err)
		}
	}
	if s.file != nil {
		t.Fatal("small final spool should remain in memory")
	}
	r, err := s.Reader()
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	for i := 0; i < 10; i++ {
		m, err := r.Next()
		if err != nil || m.Span.StartByte != i*10 {
			t.Fatalf("item %d: %+v %v", i, m, err)
		}
	}
	if _, err := r.Next(); err != io.EOF {
		t.Fatalf("unexpected tail: %v", err)
	}
}

func TestR07S14FinalCompactCorruptionFailsClosed(t *testing.T) {
	t.Run("truncated", func(t *testing.T) {
		s, _ := NewFinalCompact()
		defer s.Close()
		for i := 0; i < memoryRecords+1; i++ {
			if err := s.Append(finalFixture(i)); err != nil {
				t.Fatal(err)
			}
		}
		if err := s.writer.Flush(); err != nil {
			t.Fatal(err)
		}
		stat, err := s.file.Stat()
		if err != nil {
			t.Fatal(err)
		}
		if err := s.file.Truncate(stat.Size() - 4); err != nil {
			t.Fatal(err)
		}
		r, err := s.Reader()
		if err != nil {
			t.Fatal(err)
		}
		defer r.Close()
		for i := 0; i < memoryRecords; i++ {
			if _, err := r.Next(); err != nil {
				t.Fatal(err)
			}
		}
		if _, err := r.Next(); err == nil || err == io.EOF {
			t.Fatalf("silent truncation: %v", err)
		}
	})
	t.Run("unexpected trailing", func(t *testing.T) {
		s, _ := NewFinalCompact()
		defer s.Close()
		for i := 0; i < memoryRecords+1; i++ {
			if err := s.Append(finalFixture(i)); err != nil {
				t.Fatal(err)
			}
		}
		if err := s.writer.Flush(); err != nil {
			t.Fatal(err)
		}
		if _, err := s.file.Write([]byte("extra")); err != nil {
			t.Fatal(err)
		}
		r, err := s.Reader()
		if err != nil {
			t.Fatal(err)
		}
		defer r.Close()
		for i := 0; i < memoryRecords+1; i++ {
			if _, err := r.Next(); err != nil {
				t.Fatal(err)
			}
		}
		if _, err := r.Next(); err == nil || err == io.EOF {
			t.Fatalf("unexpected trailing data accepted: %v", err)
		}
	})
	t.Run("spill failure preserves records", func(t *testing.T) {
		s, _ := NewFinalCompact()
		defer s.Close()
		if err := s.Append(finalFixture(1)); err != nil {
			t.Fatal(err)
		}
		t.Setenv("TMPDIR", filepath.Join(t.TempDir(), "missing"))
		err := s.Append(model.Mutation{Diff: strings.Repeat("x", memoryBytes)})
		if err == nil {
			t.Fatal("expected spill error")
		}
		if s.Len() != 1 || s.file != nil {
			t.Fatal("lost previous record on spill failure")
		}
		r, err := s.Reader()
		if err != nil {
			t.Fatal(err)
		}
		defer r.Close()
		got, err := r.Next()
		if err != nil || !reflect.DeepEqual(got, finalFixture(1)) {
			t.Fatalf("lost record %+v %v", got, err)
		}
	})
}
