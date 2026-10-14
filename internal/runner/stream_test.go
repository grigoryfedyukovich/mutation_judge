package runner

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/example/mutation-judge/internal/model"
)

func writeStreamEvent(t *testing.T, s *jsonEventStream, e testEvent) {
	t.Helper()
	row, err := json.Marshal(e)
	if err != nil {
		t.Fatal(err)
	}
	row = append(row, '\n')
	// Deliberately split records: os/exec stdout Writer may get arbitrary chunks.
	for len(row) > 0 {
		chunk := 17
		if len(row) < chunk {
			chunk = len(row)
		}
		n, err := s.Write(row[:chunk])
		if err != nil || n != chunk {
			t.Fatalf("write n=%d err=%v", n, err)
		}
		row = row[chunk:]
	}
}

func TestStreamingVerdictAfterManyOutputEvents(t *testing.T) {
	s := newJSONEventStream()
	writeStreamEvent(t, s, testEvent{Action: "run", Package: "example/p", Test: "TestLate"})
	const chunks = 12000
	for i := 0; i < chunks; i++ {
		writeStreamEvent(t, s, testEvent{Action: "output", Package: "example/p", Test: "TestLate", Output: strings.Repeat("X", 2048) + "\n"})
	}
	writeStreamEvent(t, s, testEvent{Action: "fail", Package: "example/p", Test: "TestLate"})
	s.finish()
	verdict, tests := s.classify()
	if verdict != model.VerdictKilled || len(tests) != 1 || tests[0] != "TestLate" {
		t.Fatalf("verdict=%s tests=%v", verdict, tests)
	}
	if !s.outputTruncated(boundedText{limit: maxStderrBytes}) {
		t.Fatal("missing truncation signal")
	}
	if len(s.display.data) > maxOutputBytes || s.evidenceBytes > maxEvidenceBytes || len(s.evidence) > maxEvidenceEvents {
		t.Fatalf("unbounded capture: display=%d evidence=%d events=%d", len(s.display.data), s.evidenceBytes, len(s.evidence))
	}
	if len(s.line) != 0 || len(s.started) != 0 {
		t.Fatalf("pending state: line=%d started=%d", len(s.line), len(s.started))
	}
	if s.eventCount != chunks+2 {
		t.Fatalf("events=%d want %d", s.eventCount, chunks+2)
	}
}

func TestStreamingOversizedJSONFailsClosedUnlessKillObserved(t *testing.T) {
	for _, explicitKill := range []bool{false, true} {
		s := newJSONEventStream()
		writeStreamEvent(t, s, testEvent{Action: "output", Package: "p", Test: "TestA", Output: strings.Repeat("Z", maxJSONEventBytes)})
		if explicitKill {
			writeStreamEvent(t, s, testEvent{Action: "fail", Package: "p", Test: "TestA"})
		}
		s.finish()
		v, tests := s.classify()
		want := model.VerdictUnknown
		if explicitKill {
			want = model.VerdictKilled
		}
		if v != want {
			t.Fatalf("explicitKill=%v got=%s want=%s tests=%v", explicitKill, v, want, tests)
		}
	}
}

func TestStreamingBoundedStderrAndEvidence(t *testing.T) {
	s := newJSONEventStream()
	errOut := boundedText{limit: maxStderrBytes}
	input := bytes.Repeat([]byte("e"), 5<<20)
	n, err := errOut.Write(input)
	if err != nil || n != len(input) || len(errOut.data) != maxStderrBytes {
		t.Fatalf("stderr count=%d retained=%d error=%v", n, len(errOut.data), err)
	}
	writeStreamEvent(t, s, testEvent{Action: "run", Package: "p", Test: "TestA"})
	writeStreamEvent(t, s, testEvent{Action: "output", Package: "p", Test: "TestA", Output: "    p_test.go:8: value mismatch\n"})
	writeStreamEvent(t, s, testEvent{Action: "fail", Package: "p", Test: "TestA"})
	s.finish()
	got := s.diagnosticOutput(errOut)
	if len(got) > maxOutputBytes+128 || !strings.Contains(got, "truncated") {
		t.Fatalf("unbounded or unmarked report %d bytes", len(got))
	}
	v, tests := s.classify()
	if v != model.VerdictKilled || len(tests) != 1 {
		t.Fatalf("verdict=%s tests=%v", v, tests)
	}
	a := extractAssertions(s.evidence, tests)
	if len(a) != 1 || a[0].Message != "value mismatch" {
		t.Fatalf("assertions=%+v", a)
	}
}

func TestStreamingTruncatedTestStateDoesNotAssumeKill(t *testing.T) {
	s := newJSONEventStream()
	for i := 0; i < maxTrackedTests+1; i++ {
		writeStreamEvent(t, s, testEvent{Action: "run", Package: "p", Test: fmt.Sprintf("Test%d", i)})
	}
	// Simulate successful teardown of all tracked tests, except the name which
	// could not be retained. An incomplete state must never be treated as a
	// trustworthy full set of in-flight tests.
	for i := 0; i < maxTrackedTests; i++ {
		writeStreamEvent(t, s, testEvent{Action: "pass", Package: "p", Test: fmt.Sprintf("Test%d", i)})
	}
	s.finish()
	v, _ := s.classify()
	if v != model.VerdictUnknown {
		t.Fatalf("verdict=%s want UNKNOWN", v)
	}
}

func TestGoTestStreamingHugeOutputRetainsFailureAndBoundsReport(t *testing.T) {
	if testing.Short() {
		t.Skip("go test integration probe")
	}
	src := `package p
import("fmt";"os";"strings";"testing")
func TestFlood(t *testing.T) {
 for i:=0;i<1500;i++ {fmt.Println(strings.Repeat("x",2048))}
 fmt.Fprintln(os.Stderr,strings.Repeat("y",2<<20))
 t.Fatalf("late failure from large stream")
}`
	root := testModule(t, "package p\nfunc F(){}\n", src)
	res := (GoTest{}).Run(context.Background(), Request{Root: root, WorkRel: ".", Patterns: []string{"."}, Timeout: 20 * time.Second})
	if res.Verdict != model.VerdictKilled || len(res.Tests) != 1 || res.Tests[0] != "TestFlood" {
		t.Fatalf("verdict=%s tests=%v output prefix=%.500s", res.Verdict, res.Tests, res.Output)
	}
	if !res.OutputTruncated || len(res.Output) > maxOutputBytes+128 {
		t.Fatalf("missing bound: truncated=%v output=%d", res.OutputTruncated, len(res.Output))
	}
}

func TestGoTestStreamingNoTestsButSetupError(t *testing.T) {
	root := testModule(t, "package p\nfunc F() int{return missing}\n", "")
	res := (GoTest{}).Run(context.Background(), Request{Root: root, WorkRel: ".", Patterns: []string{"."}, Timeout: 5 * time.Second})
	if res.Verdict != model.VerdictInvalid {
		t.Fatalf("verdict=%s output=%.200s", res.Verdict, res.Output)
	}
}
