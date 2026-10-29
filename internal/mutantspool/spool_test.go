package mutantspool

import (
	"github.com/example/mutation-judge/internal/model"
	"io"
	"os"
	"path/filepath"
	"testing"
)

func TestSpoolReaderAndCleanup(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("TMPDIR", tmp)
	s, err := New()
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 1000; i++ {
		if err := s.Append(model.Mutation{Span: model.Span{StartByte: i}}); err != nil {
			t.Fatal(err)
		}
	}
	r, err := s.Reader()
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 1000; i++ {
		m, err := r.Next()
		if err != nil || m.Span.StartByte != i {
			t.Fatalf("item %d: %+v %v", i, m, err)
		}
	}
	if _, err := r.Next(); err != io.EOF {
		t.Fatalf("expected EOF: %v", err)
	}
	_ = r.Close()
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	files, err := os.ReadDir(tmp)
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 0 {
		t.Fatalf("spool not removed from %s: %v", filepath.Clean(tmp), files)
	}
}
