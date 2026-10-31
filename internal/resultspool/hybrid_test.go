package resultspool

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/example/mutation-judge/internal/model"
)

func TestR07S8SmallResultStreamAvoidsFilesAndFreezesRecords(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("TMPDIR", tmp)
	s, err := New()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	if err := s.Append(model.Result{Output: "old", Verdict: model.VerdictKilled}); err != nil {
		t.Fatal(err)
	}
	if s.file != nil {
		t.Fatal("small spool created a file")
	}
	files, err := os.ReadDir(tmp)
	if err != nil || len(files) != 0 {
		t.Fatalf("temp files=%v err=%v", files, err)
	}
	for pass := 0; pass < 2; pass++ {
		if err := s.ForEach(func(r model.Result) error {
			if r.Output != "old" || r.Verdict != model.VerdictKilled {
				t.Fatalf("unexpected result %+v", r)
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
	}
	want := errors.New("stop")
	if err := s.ForEach(func(model.Result) error { return want }); !errors.Is(err, want) {
		t.Fatalf("error=%v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if err := s.ForEach(func(model.Result) error { return nil }); err == nil {
		t.Fatal("read succeeded after Close")
	}
}

func TestR07S8ResultSpillByCountAndBytes(t *testing.T) {
	for _, tc := range []struct {
		name   string
		count  int
		output string
	}{
		{name: "count", count: memoryRecords + 1, output: "small"},
		{name: "bytes", count: 1, output: strings.Repeat("X", memoryBytes)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tmp := t.TempDir()
			t.Setenv("TMPDIR", tmp)
			s, err := New()
			if err != nil {
				t.Fatal(err)
			}
			for i := 0; i < tc.count; i++ {
				if err := s.Append(model.Result{Output: tc.output, DurationMS: int64(i)}); err != nil {
					t.Fatal(err)
				}
			}
			if s.file == nil {
				t.Fatal("expected disk spill")
			}
			path := s.file.Name()
			for pass := 0; pass < 2; pass++ {
				i := 0
				if err := s.ForEach(func(r model.Result) error {
					if r.Output != tc.output || r.DurationMS != int64(i) {
						t.Fatalf("out of order at %d", i)
					}
					i++
					return nil
				}); err != nil {
					t.Fatal(err)
				}
				if i != tc.count {
					t.Fatalf("visited %d want %d", i, tc.count)
				}
			}
			if err := s.Close(); err != nil {
				t.Fatal(err)
			}
			if _, err := os.Stat(path); !os.IsNotExist(err) {
				t.Fatalf("spill still exists: %v", err)
			}
		})
	}
}

func TestR07S8ResultSpillFailurePreservesBufferedRecords(t *testing.T) {
	s, err := New()
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.Append(model.Result{Output: "retained"}); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TMPDIR", filepath.Join(t.TempDir(), "nonexistent"))
	if err := s.Append(model.Result{Output: strings.Repeat("x", memoryBytes)}); err == nil {
		t.Fatal("expected spill failure")
	}
	if s.Len() != 1 || s.file != nil {
		t.Fatalf("bad state after failed spill: %d", s.Len())
	}
	if err := s.ForEach(func(r model.Result) error {
		if r.Output != "retained" {
			t.Fatal("lost previous record")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}
