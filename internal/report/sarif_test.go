package report

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/example/mutation-judge/internal/model"
)

func sampleReportForFormats() model.Report {
	return model.Report{
		ToolVersion: "test", GeneratedAt: time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC),
		Complete: true, Patterns: []string{"./pkg"},
		Bounds:  map[string]any{"max_mutants": 0, "per_mutant_timeout": "20s"},
		Summary: model.Summary{Generated: 4, Killed: 1, Survived: 1, Invalid: 1, Timeout: 1, ScoreText: "50.0% excluding invalid/timeout/unknown/unsupported"},
		Timing:  model.Timing{RenderingMS: 3, TotalMS: 9},
		Results: []model.Result{
			{
				Verdict: model.VerdictSurvived, Covered: true, CoverageKnown: true,
				Mutation: model.Mutation{
					ID: "M-survived", RuleID: "MJ-BOUNDARY", Operator: "boundary",
					Span:        model.Span{File: "pkg/demo.go", StartLine: 3, StartCol: 5, EndLine: 3, EndCol: 6},
					Description: "replace comparison > with >=", Suggestion: "exercise n == 0",
				},
			},
			{
				Verdict: model.VerdictKilled, Responsible: []string{"TestFoo"},
				Mutation: model.Mutation{ID: "M-killed", RuleID: "MJ-BOUNDARY", Operator: "boundary", Span: model.Span{File: "pkg/demo.go", StartLine: 8, StartCol: 1}},
			},
			{
				Verdict:  model.VerdictInvalid,
				Mutation: model.Mutation{ID: "M-invalid", RuleID: "MJ-ARITHMETIC", Operator: "arithmetic", Span: model.Span{File: "pkg/demo.go", StartLine: 12, StartCol: 1}},
			},
			{
				Verdict: model.VerdictTimeout,
				Mutation: model.Mutation{
					ID: "M-timeout", RuleID: "MJ-LOOP-COND-FALSE", Operator: "loop",
					Span: model.Span{File: "pkg/loop.go", StartLine: 20, StartCol: 2}, Description: "force the loop condition false",
				},
			},
		},
	}
}

func TestSARIFOnlyIncludesActionableVerdicts(t *testing.T) {
	var b bytes.Buffer
	if err := Render(&b, "sarif", sampleReportForFormats()); err != nil {
		t.Fatal(err)
	}
	var log sarifLog
	if err := json.Unmarshal(b.Bytes(), &log); err != nil {
		t.Fatalf("output is not valid JSON: %v\n%s", err, b.String())
	}
	if len(log.Runs) != 1 {
		t.Fatalf("expected exactly 1 run, got %d", len(log.Runs))
	}
	results := log.Runs[0].Results
	if len(results) != 2 {
		t.Fatalf("expected 2 results (survived + timeout only), got %d: %#v", len(results), results)
	}
	byID := map[string]sarifResult{}
	for _, r := range results {
		byID[r.PartialFingerprints["mutationJudgeMutantId/v1"]] = r
	}
	if _, ok := byID["M-killed"]; ok {
		t.Fatal("KILLED must not appear in SARIF results")
	}
	if _, ok := byID["M-invalid"]; ok {
		t.Fatal("INVALID must not appear in SARIF results")
	}
	survived, ok := byID["M-survived"]
	if !ok {
		t.Fatal("expected the survived mutant in results")
	}
	if survived.Level != "warning" {
		t.Fatalf("survived level = %q, want warning", survived.Level)
	}
	if !strings.Contains(survived.Message.Text, "exercise n == 0") {
		t.Fatalf("message missing suggestion: %q", survived.Message.Text)
	}
	timeout, ok := byID["M-timeout"]
	if !ok {
		t.Fatal("expected the timeout mutant in results")
	}
	if timeout.Level != "note" {
		t.Fatalf("timeout level = %q, want note", timeout.Level)
	}
}

func TestSARIFSchemaAndVersionFields(t *testing.T) {
	var b bytes.Buffer
	if err := Render(&b, "sarif", sampleReportForFormats()); err != nil {
		t.Fatal(err)
	}
	var raw map[string]any
	if err := json.Unmarshal(b.Bytes(), &raw); err != nil {
		t.Fatal(err)
	}
	if raw["version"] != "2.1.0" {
		t.Fatalf("version = %v, want 2.1.0", raw["version"])
	}
	schema, _ := raw["$schema"].(string)
	if !strings.HasPrefix(schema, "https://") {
		t.Fatalf("$schema should be an https URL, got %q", schema)
	}
}

func TestSARIFEmptyResultsIsAnEmptyArrayNotNull(t *testing.T) {
	r := sampleReportForFormats()
	r.Results = r.Results[1:3] // killed + invalid only, nothing actionable
	var b bytes.Buffer
	if err := Render(&b, "sarif", r); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(b.String(), `"results": null`) {
		t.Fatalf("results must serialize as [], not null: %s", b.String())
	}
	if !strings.Contains(b.String(), `"results": []`) {
		t.Fatalf("expected an empty results array: %s", b.String())
	}
}

func TestSARIFRulesDeduplicatedAndSorted(t *testing.T) {
	r := sampleReportForFormats()
	// Add a second survived mutant reusing the same rule ID as the first.
	dup := r.Results[0]
	dup.Mutation.ID = "M-survived-2"
	r.Results = append(r.Results, dup)
	var b bytes.Buffer
	if err := Render(&b, "sarif", r); err != nil {
		t.Fatal(err)
	}
	var log sarifLog
	if err := json.Unmarshal(b.Bytes(), &log); err != nil {
		t.Fatal(err)
	}
	rules := log.Runs[0].Tool.Driver.Rules
	seen := map[string]bool{}
	for _, ru := range rules {
		if seen[ru.ID] {
			t.Fatalf("rule %s listed more than once", ru.ID)
		}
		seen[ru.ID] = true
	}
	if !seen["MJ-BOUNDARY"] || !seen["MJ-LOOP-COND-FALSE"] {
		t.Fatalf("missing expected rules: %#v", rules)
	}
}

func TestSARIFTimingSentinelsSurviveRenderMeasured(t *testing.T) {
	r := sampleReportForFormats()
	r.Timing.TotalMS = 7
	var b bytes.Buffer
	if err := RenderMeasured(&b, "sarif", &r); err != nil {
		t.Fatal(err)
	}
	if r.Timing.TotalMS < 7 {
		t.Fatalf("timing was not patched: %#v", r.Timing)
	}
	if !json.Valid(b.Bytes()) {
		t.Fatalf("patched output is no longer valid JSON:\n%s", b.String())
	}
}
