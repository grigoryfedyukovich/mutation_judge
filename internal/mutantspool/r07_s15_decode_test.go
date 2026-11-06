package mutantspool

import (
	"fmt"
	"io"
	"os"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/example/mutation-judge/internal/model"
)

// Preserve ownership even when the buffered reader recycles its storage. This
// also checks the larger-than-Peek path and the in-memory spool path.
func TestR07S15DecodedStringsRemainOwned(t *testing.T) {
	for _, size := range []int{1, 32<<10 - 100, 32 << 10, 32<<10 + 1, 96 << 10, 1 << 20} {
		t.Run(fmt.Sprintf("size-%d", size), func(t *testing.T) {
			spool, err := NewFinalCompact()
			if err != nil {
				t.Fatal(err)
			}
			defer spool.Close()
			first := compactFixture(1)
			first.Diff = strings.Repeat("λ", size/2)
			if err := spool.Append(first); err != nil {
				t.Fatal(err)
			}
			// Force a spill, several buffer refills, and a second reader pass.
			for i := 0; i < memoryRecords+20; i++ {
				next := compactFixture(i + 2)
				next.ID = fmt.Sprintf("M-%012x", i+2)
				next.Diff = strings.Repeat("x", 800+(i%23))
				if err := spool.Append(next); err != nil {
					t.Fatal(err)
				}
			}
			r, err := spool.Reader()
			if err != nil {
				t.Fatal(err)
			}
			got, err := r.Next()
			if err != nil {
				t.Fatal(err)
			}
			for i := 0; i < memoryRecords+20; i++ {
				if _, err := r.Next(); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := r.Next(); err != io.EOF {
				t.Fatalf("unexpected tail: %v", err)
			}
			if err := r.Close(); err != nil {
				t.Fatal(err)
			}
			runtime.GC()
			if !reflect.DeepEqual(got, first) {
				t.Fatal("decoded strings were corrupted by later reads")
			}
			second, err := spool.Reader()
			if err != nil {
				t.Fatal(err)
			}
			reread, err := second.Next()
			if err != nil || !reflect.DeepEqual(reread, first) {
				t.Fatalf("replay differs: %v", err)
			}
			_ = second.Close()
		})
	}
}

func TestR07S15LargeRecordTruncationIsError(t *testing.T) {
	for _, size := range []int{200, 32 << 10, 1 << 20} {
		t.Run(fmt.Sprintf("size-%d", size), func(t *testing.T) {
			spool, err := NewFinalCompact()
			if err != nil {
				t.Fatal(err)
			}
			defer spool.Close()
			first := compactFixture(1)
			first.Diff = strings.Repeat("a", size)
			if err := spool.Append(first); err != nil {
				t.Fatal(err)
			}
			for i := 0; i < memoryRecords+5; i++ {
				if err := spool.Append(compactFixture(i + 2)); err != nil {
					t.Fatal(err)
				}
			}
			if err := spool.writer.Flush(); err != nil {
				t.Fatal(err)
			}
			// Truncate inside the last record: ordinary EOF is never evidence
			// of a clean end when the spool's advertised count is nonzero.
			info, err := spool.file.Stat()
			if err != nil {
				t.Fatal(err)
			}
			if err := spool.file.Truncate(info.Size() - 1); err != nil {
				t.Fatal(err)
			}
			reader, err := spool.Reader()
			if err != nil {
				t.Fatal(err)
			}
			defer reader.Close()
			for i := 0; i < memoryRecords+5; i++ {
				if _, err := reader.Next(); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := reader.Next(); err == nil || err == io.EOF {
				t.Fatalf("truncation ignored: %v", err)
			}
		})
	}
}

// Optional large-run fixture for reproducible external peak-RSS measurement:
// go test -c ./internal/mutantspool -o /tmp/mutantspool.test
// /usr/bin/time -v env MUTATION_JUDGE_R07_S15_RSS=1 /tmp/mutantspool.test -test.run '^TestR07S15LargeSpoolRSSFixture$' -test.v
func TestR07S15LargeSpoolRSSFixture(t *testing.T) {
	if os.Getenv("MUTATION_JUDGE_R07_S15_RSS") != "1" {
		t.Skip("opt-in large-run measurement")
	}
	const count = 131072
	spool, err := NewFinalCompact()
	if err != nil {
		t.Fatal(err)
	}
	defer spool.Close()
	diff := strings.Repeat("d", 640)
	began := time.Now()
	for i := 0; i < count; i++ {
		m := model.Mutation{
			ID: fmt.Sprintf("M-%012x", i), Operator: "boundary", RuleID: "boundary",
			Span:     model.Span{File: "pkg/f.go", StartByte: i * 10, EndByte: i*10 + 1, StartLine: i + 1},
			Original: ">", Replacement: ">=", Description: "Change boundary", Diff: diff,
		}
		if err := spool.Append(m); err != nil {
			t.Fatal(err)
		}
	}
	appendTime := time.Since(began)
	began = time.Now()
	for pass := 0; pass < 2; pass++ {
		reader, err := spool.Reader()
		if err != nil {
			t.Fatal(err)
		}
		for i := 0; i < count; i++ {
			m, err := reader.Next()
			if err != nil {
				t.Fatalf("pass %d item %d: %v", pass, i, err)
			}
			if m.Span.StartByte != i*10 || m.ID != fmt.Sprintf("M-%012x", i) || m.Diff != diff {
				t.Fatalf("pass %d item %d changed", pass, i)
			}
		}
		if _, err := reader.Next(); err != io.EOF {
			t.Fatalf("tail: %v", err)
		}
		if err := reader.Close(); err != nil {
			t.Fatal(err)
		}
	}
	t.Logf("records=%d append=%v two_reads=%v", count, appendTime, time.Since(began))
}
