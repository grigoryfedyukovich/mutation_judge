package mutantspool

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/example/mutation-judge/internal/model"
)

func TestR07S8SmallMutationStreamAvoidsFiles(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("TMPDIR", tmp)
	s, err := New()
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	for i := 0; i < 6; i++ {
		if err := s.Append(model.Mutation{ID: string(rune('a' + i)), Span: model.Span{StartByte: i}}); err != nil {
			t.Fatal(err)
		}
	}
	if s.file != nil {
		t.Fatal("small mutation spool created a file")
	}
	files, err := os.ReadDir(tmp)
	if err != nil || len(files) != 0 {
		t.Fatalf("temp files=%v err=%v", files, err)
	}
	for pass := 0; pass < 2; pass++ {
		r, err := s.Reader()
		if err != nil {
			t.Fatal(err)
		}
		for i := 0; i < 6; i++ {
			m, err := r.Next()
			if err != nil || m.Span.StartByte != i {
				t.Fatalf("record %d: %+v %v", i, m, err)
			}
		}
		if _, err := r.Next(); err != io.EOF {
			t.Fatalf("expected EOF, got %v", err)
		}
		if err := r.Close(); err != nil {
			t.Fatal(err)
		}
		if _, err := r.Next(); err == nil {
			t.Fatal("closed reader succeeded")
		}
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Reader(); err == nil {
		t.Fatal("closed spool readable")
	}
}

func TestR07S8MutationSpillByCountAndBytes(t *testing.T) {
	for _, tc := range []struct {
		name        string
		count       int
		replacement string
	}{
		{name: "count", count: memoryRecords + 1, replacement: "x"},
		{name: "bytes", count: 1, replacement: strings.Repeat("x", memoryBytes)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, err := New()
			if err != nil {
				t.Fatal(err)
			}
			for i := 0; i < tc.count; i++ {
				if err := s.Append(model.Mutation{Replacement: tc.replacement, Span: model.Span{StartByte: i}}); err != nil {
					t.Fatal(err)
				}
			}
			if s.file == nil {
				t.Fatal("large mutation spool stayed in memory")
			}
			name := s.file.Name()
			for pass := 0; pass < 2; pass++ {
				r, err := s.Reader()
				if err != nil {
					t.Fatal(err)
				}
				for i := 0; i < tc.count; i++ {
					m, err := r.Next()
					if err != nil || m.Span.StartByte != i || m.Replacement != tc.replacement {
						t.Fatalf("record %d: %+v %v", i, m, err)
					}
				}
				if _, err := r.Next(); err != io.EOF {
					t.Fatalf("tail err=%v", err)
				}
				if err := r.Close(); err != nil {
					t.Fatal(err)
				}
			}
			if err := s.Close(); err != nil {
				t.Fatal(err)
			}
			if _, err := os.Stat(name); !os.IsNotExist(err) {
				t.Fatalf("file leaked: %v", err)
			}
		})
	}
}

func TestR07S8MutationSpillFailurePreservesRecords(t *testing.T) {
	s, err := New()
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.Append(model.Mutation{ID: "kept"}); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TMPDIR", filepath.Join(t.TempDir(), "missing"))
	if err := s.Append(model.Mutation{Replacement: strings.Repeat("x", memoryBytes)}); err == nil {
		t.Fatal("expected failure")
	}
	if s.Len() != 1 || s.file != nil {
		t.Fatal("invalid partial transition")
	}
	r, err := s.Reader()
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	m, err := r.Next()
	if err != nil || m.ID != "kept" {
		t.Fatalf("record=%+v err=%v", m, err)
	}
}
