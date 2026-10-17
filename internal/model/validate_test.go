package model

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func fixtureReport() Report {
	mutation := Mutation{
		ID: "M-1", Operator: "boundary", RuleID: "boundary-v1",
		Span:     Span{File: "p.go", StartByte: 20, EndByte: 21, StartLine: 2, EndLine: 2, StartCol: 12, EndCol: 13},
		Original: ">", Replacement: ">=", Description: "change boundary", Suggestion: "check zero", Diff: "@@ -2 +2 @@\n-a > b\n+a >= b\n",
	}
	return Report{
		SchemaVersion: SchemaVersion, ToolVersion: "0.1.3", GoVersion: "go1.23.2", GeneratedAt: time.Now().UTC(),
		Complete: true, Patterns: []string{"./..."}, EffectiveConfig: map[string]any{"operators": []string{"boundary"}},
		Bounds:    map[string]any{"discovered_before_bound": 1, "retained_after_bound": 1, "completed_mutants": 1},
		Semantics: []string{"One mutant per run."},
		Results:   []Result{{Mutation: mutation, Verdict: VerdictKilled, Diagnostic: Diagnostic{ID: "MJ-VERDICT-KILLED", Statement: "killed", Location: mutation.Span}}},
		Summary:   Summary{Generated: 1, Killed: 1, Score: 100, ScoreText: "100.0% excluding invalid/timeout/unknown/unsupported/equivalent"},
	}
}

func encodeForTest(t *testing.T, r Report) []byte {
	t.Helper()
	b, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestDecodeReportValidAndIncomplete(t *testing.T) {
	r := fixtureReport()
	b := encodeForTest(t, r)
	got, err := DecodeReport(bytes.NewReader(append(b, '\n', ' ', '\n')))
	if err != nil || got.Summary.Killed != 1 {
		t.Fatalf("normal report: %+v %v", got.Summary, err)
	}

	// The analyzer may legitimately stop mid-run. That is a valid report,
	// but F09 comparison compatibility must still reject it as an oracle.
	r.Complete = false
	r.Bounds["discovered_before_bound"] = 2
	r.Bounds["retained_after_bound"] = 2
	if _, err := DecodeReport(bytes.NewReader(encodeForTest(t, r))); err != nil {
		t.Fatalf("incomplete report rejected: %v", err)
	}

	// A max-mutants bound limits discovery without making the retained set
	// incomplete. It is a valid imported report, but F09 flags incompatibility.
	r.Complete = true
	r.Bounds["retained_after_bound"] = 1
	if _, err := DecodeReport(bytes.NewReader(encodeForTest(t, r))); err != nil {
		t.Fatalf("bounded report rejected: %v", err)
	}

	// Empty discovery is a genuine report; empty results sometimes encode
	// as null when produced by an early/parallel zero-mutant path.
	r.Results = nil
	r.Bounds["retained_after_bound"] = 0
	r.Bounds["completed_mutants"] = 0
	r.Summary = Summary{ScoreText: "n/a (no scoreable mutants)"}
	if _, err := DecodeReport(bytes.NewReader(encodeForTest(t, r))); err != nil {
		t.Fatalf("empty result report rejected: %v", err)
	}
}

func TestDecodeReportRejectsCorruption(t *testing.T) {
	original := fixtureReport()
	base := encodeForTest(t, original)
	var root map[string]any
	if err := json.Unmarshal(base, &root); err != nil {
		t.Fatal(err)
	}
	clone := func() map[string]any {
		var out map[string]any
		if err := json.Unmarshal(base, &out); err != nil {
			t.Fatal(err)
		}
		return out
	}
	result := func(m map[string]any) map[string]any { return m["results"].([]any)[0].(map[string]any) }
	encode := func(m map[string]any) []byte {
		b, err := json.Marshal(m)
		if err != nil {
			t.Fatal(err)
		}
		return b
	}
	tests := []struct {
		name string
		data []byte
		want string
	}{
		{"unrelated namespace", []byte(`{"schema_version":"unrelated/v1"}`), "unsupported schema_version"},
		{"suffix collision", func() []byte { m := clone(); m["schema_version"] = "unrelated/v1"; return encode(m) }(), "unsupported schema_version"},
		{"missing version", func() []byte { m := clone(); delete(m, "schema_version"); return encode(m) }(), "missing required field schema_version"},
		{"missing summary", func() []byte { m := clone(); delete(m, "summary"); return encode(m) }(), "missing required field summary"},
		{"missing summary count", func() []byte { m := clone(); delete(m["summary"].(map[string]any), "killed"); return encode(m) }(), "summary: missing required field killed"},
		{"missing result verdict", func() []byte { m := clone(); delete(result(m), "verdict"); return encode(m) }(), "missing required field verdict"},
		{"missing span", func() []byte { m := clone(); delete(result(m)["mutation"].(map[string]any), "span"); return encode(m) }(), "missing required field span"},
		{"missing diagnostic", func() []byte { m := clone(); delete(result(m), "diagnostic"); return encode(m) }(), "missing required field diagnostic"},
		{"duplicate mutant IDs", func() []byte {
			m := clone()
			m["results"] = append(m["results"].([]any), result(m))
			m["summary"].(map[string]any)["generated"] = 2
			m["summary"].(map[string]any)["killed"] = 2
			m["bounds"].(map[string]any)["retained_after_bound"] = 2
			m["bounds"].(map[string]any)["completed_mutants"] = 2
			m["bounds"].(map[string]any)["discovered_before_bound"] = 2
			return encode(m)
		}(), "duplicate mutant ID"},
		{"invalid verdict", func() []byte { m := clone(); result(m)["verdict"] = "MAYBE"; return encode(m) }(), "invalid verdict"},
		{"negative summary", func() []byte { m := clone(); m["summary"].(map[string]any)["killed"] = -1; return encode(m) }(), "summary counts"},
		{"fractional summary", func() []byte { m := clone(); m["summary"].(map[string]any)["generated"] = 1.5; return encode(m) }(), "decode report fields"},
		{"summary mismatch", func() []byte { m := clone(); m["summary"].(map[string]any)["killed"] = 0; return encode(m) }(), "summary counts"},
		{"score mismatch", func() []byte { m := clone(); m["summary"].(map[string]any)["score"] = 80; return encode(m) }(), "summary score"},
		{"fractional bounds", func() []byte { m := clone(); m["bounds"].(map[string]any)["completed_mutants"] = 1.5; return encode(m) }(), "bounds.completed_mutants"},
		{"exponent bounds", func() []byte {
			return bytes.Replace(base, []byte(`"completed_mutants":1`), []byte(`"completed_mutants":1e0`), 1)
		}(), "bounds.completed_mutants"},
		{"overflow bounds", func() []byte {
			return bytes.Replace(base, []byte(`"discovered_before_bound":1`), []byte(`"discovered_before_bound":9223372036854775808`), 1)
		}(), "bounds.discovered_before_bound"},
		{"negative bounds", func() []byte { m := clone(); m["bounds"].(map[string]any)["completed_mutants"] = -1; return encode(m) }(), "bounds.completed_mutants"},
		{"inconsistent bounds", func() []byte { m := clone(); m["bounds"].(map[string]any)["completed_mutants"] = 0; return encode(m) }(), "inconsistent bounds"},
		{"false complete", func() []byte {
			m := clone()
			m["bounds"].(map[string]any)["retained_after_bound"] = 2
			m["bounds"].(map[string]any)["discovered_before_bound"] = 2
			return encode(m)
		}(), "complete report"},
		{"missing patterns", func() []byte { m := clone(); m["patterns"] = []any{}; return encode(m) }(), "patterns must identify"},
		{"empty config", func() []byte { m := clone(); m["effective_config"] = map[string]any{}; return encode(m) }(), "effective_config and semantics must be nonempty"},
		{"empty semantics", func() []byte { m := clone(); m["semantics"] = []string{}; return encode(m) }(), "effective_config and semantics must be nonempty"},
		{"missing date", func() []byte { m := clone(); m["generated_at"] = "0001-01-01T00:00:00Z"; return encode(m) }(), "generated_at"},
		{"invalid span", func() []byte {
			m := clone()
			result(m)["mutation"].(map[string]any)["span"].(map[string]any)["end_byte"] = 20
			return encode(m)
		}(), "invalid mutation source span"},
		{"negative timing", func() []byte { m := clone(); m["timing"].(map[string]any)["total_ms"] = -1; return encode(m) }(), "negative timing"},
		{"second document", append(append([]byte{}, base...), base...), "unexpected JSON after report"},
		{"trailing garbage", append(append([]byte{}, base...), []byte(" xyz")...), "trailing data after report"},
		{"null top-level", []byte("null"), "report must be a JSON object"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := DecodeReport(bytes.NewReader(tt.data))
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("expected error containing %q, got %v", tt.want, err)
			}
		})
	}
}
