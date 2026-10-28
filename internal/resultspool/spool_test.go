package resultspool

import (
	"os"
	"strings"
	"testing"

	"github.com/example/mutation-judge/internal/model"
)

func TestLargeOrderedSpoolAndCleanup(t *testing.T) {
	spool, err := New()
	if err != nil {
		t.Fatal(err)
	}
	filename := spool.file.Name()
	// Larger than any single retained evidence buffer. Only one record at
	// a time is serialized and decoded, even for thousands of mutants.
	payload := strings.Repeat("evidence", 128)
	const count = 12000
	for i := 0; i < count; i++ {
		r := model.Result{Verdict: model.VerdictSurvived, Output: payload, DurationMS: int64(i)}
		if err := spool.Append(r); err != nil {
			t.Fatal(err)
		}
	}
	if spool.Len() != count {
		t.Fatalf("got %d entries", spool.Len())
	}
	observed := 0
	for pass := 0; pass < 2; pass++ {
		observed = 0
		if err := spool.ForEach(func(r model.Result) error {
			if r.DurationMS != int64(observed) || r.Output != payload {
				t.Fatalf("wrong result %d", observed)
			}
			observed++
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		if observed != count {
			t.Fatalf("read %d entries", observed)
		}
	}
	if err := spool.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filename); !os.IsNotExist(err) {
		t.Fatalf("temporary spool not removed: %v", err)
	}
	if err := spool.Append(model.Result{}); err == nil {
		t.Fatal("append succeeded after close")
	}
}
