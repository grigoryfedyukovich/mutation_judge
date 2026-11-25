package compare

import (
	"bytes"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/example/mutation-judge/internal/model"
)

func compatibleFixture(results ...model.Result) model.Report {
	for i := range results {
		if results[i].Verdict != model.VerdictEquivalent {
			results[i].Diagnostic.Evidence = map[string]any{"test_command": "go test -run '^Test' ./..."}
		}
	}
	return model.Report{
		SchemaVersion: model.SchemaVersion, ToolVersion: "test", GoVersion: "go1.22.2", Complete: true,
		Patterns: []string{"./..."},
		EffectiveConfig: map[string]any{
			"operators": []string{"boundary"}, "test_run": "", "timeout": "30s", "include_generated": false,
			"changed": "", "narrow_test_scope": false, "coverage_test_selection": false,
		},
		Bounds: map[string]any{
			"discovered_before_bound": len(results), "retained_after_bound": len(results), "completed_mutants": len(results),
			"backend_name": "go-test", "backend_version": "v1", "operator_semantics": "v15",
			"test_working_directory": ".", "test_command": "go test ./...",
			"resolved_test_packages": []string{"example.test/a"},
			"goos":                   "linux", "goarch": "amd64", "cgo_enabled": "1", "goflags": "", "execution_environment_hash": "hash1", "effective_go_config_hash": "go-config-hash1", "execution_inputs_complete": true, "per_mutant_timeout": "30s",
		}, Results: results,
	}
}

func TestF09CompleteComparableRunCanProveRemoval(t *testing.T) {
	gone := mutantResult("gone", model.VerdictSurvived, "a.go", 10)
	base := compatibleFixture(gone)
	current := compatibleFixture()
	if !InspectCompatibility(base, current).Comparable {
		t.Fatalf("should be comparable: %+v", InspectCompatibility(base, current))
	}
	got := CompareReports(base, current)
	if !got.Comparable || len(got.RemovedMutants) != 1 || len(got.NotAnalyzed) != 0 {
		t.Fatalf("removed site should be proved: %+v", got)
	}
}

func TestF09BoundedRunNeverClaimsRemoval(t *testing.T) {
	a := mutantResult("a", model.VerdictSurvived, "a.go", 1)
	b := mutantResult("b", model.VerdictSurvived, "a.go", 2)
	base := compatibleFixture(a, b)
	cur := compatibleFixture(a)
	cur.Bounds["discovered_before_bound"] = 2
	cur.Bounds["retained_after_bound"] = 1
	cur.Bounds["completed_mutants"] = 1
	cur.Bounds["max_mutants"] = 1
	got := CompareReports(base, cur)
	if got.Comparable || len(got.RemovedMutants) != 0 || len(got.NotAnalyzed) != 1 || got.NotAnalyzed[0].ID != "b" {
		t.Fatalf("false removal from --max-mutants: %+v", got)
	}
	if got.NotAnalyzed[0].Current != nil {
		t.Fatal("missing current result must remain explicitly absent")
	}
	var out bytes.Buffer
	if err := RenderJSON(&out, got); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), `"comparable": false`) || !strings.Contains(out.String(), `"not_analyzed": [`) || !strings.Contains(out.String(), `"unverified_changes": []`) {
		t.Fatalf("missing provenance in JSON: %s", out.String())
	}
	out.Reset()
	if err := RenderText(&out, got); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "EXPLORATORY") || !strings.Contains(out.String(), "not analysed / absence unproven: 1") {
		t.Fatalf("missing evidence warning: %s", out.String())
	}
	out.Reset()
	if err := RenderHTML(&out, got); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "Exploratory comparison") || !strings.Contains(out.String(), "Not analysed") {
		t.Fatal("HTML missing safety warnings")
	}
}

func TestF09IncompleteAndDifferentScopesAreExploratory(t *testing.T) {
	a := mutantResult("a", model.VerdictSurvived, "a.go", 1)
	b := mutantResult("b", model.VerdictKilled, "a.go", 2)
	base := compatibleFixture(a, b)
	checks := map[string]func(*model.Report){
		"cancelled": func(r *model.Report) {
			r.Complete = false
			r.Bounds["completed_mutants"] = 1
			r.Results = r.Results[:1]
		},
		"operators":          func(r *model.Report) { r.EffectiveConfig["operators"] = []string{"negate"} },
		"test scope":         func(r *model.Report) { r.Patterns = []string{"./a"} },
		"coverage selection": func(r *model.Report) { r.EffectiveConfig["coverage_test_selection"] = true },
		"environment":        func(r *model.Report) { r.Bounds["execution_environment_hash"] = "other" },
		"go configuration":   func(r *model.Report) { r.Bounds["effective_go_config_hash"] = "other" },
		"tool version":       func(r *model.Report) { r.ToolVersion = "different" },
		"per-mutant command": func(r *model.Report) { r.Results[0].Diagnostic.Evidence["test_command"] = "go test ./a" },
		"incremental":        func(r *model.Report) { r.EffectiveConfig["changed"] = "HEAD" },
		"missing provenance": func(r *model.Report) { delete(r.Bounds, "resolved_test_packages") },
	}
	for name, change := range checks {
		t.Run(name, func(t *testing.T) {
			current := compatibleFixture(a, b)
			change(&current)
			c := InspectCompatibility(base, current)
			if c.Comparable || len(c.Issues) == 0 {
				t.Fatalf("must reject %s: %+v", name, c)
			}
			if CompatibilityError(c) == nil {
				t.Fatal("expected explanation")
			}
			d := CompareReports(base, current)
			if d.Comparable || len(d.CompatibilityIssues) == 0 {
				t.Fatalf("missing warning: %+v", d)
			}
		})
	}
}

func TestF09DifferentTestScopesDoNotProveFixedSurvivor(t *testing.T) {
	a := mutantResult("a", model.VerdictSurvived, "a.go", 1)
	base := compatibleFixture(a)
	cur := compatibleFixture(mutantResult("a", model.VerdictKilled, "a.go", 1))
	cur.EffectiveConfig["test_run"] = "TestBoundary"
	d := CompareReports(base, cur)
	if d.Comparable || len(d.FixedSurvivors) != 0 || len(d.UnverifiedChanges) != 1 || d.UnverifiedChanges[0].ID != "a" {
		t.Fatalf("scope change falsely credited as a fix: %+v", d)
	}
}

func TestF09ReportCompatibilityRoundTrip(t *testing.T) {
	r := compatibleFixture(mutantResult("a", model.VerdictKilled, "a.go", 1))
	encoded, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	var restored model.Report
	if err := json.Unmarshal(encoded, &restored); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(InspectCompatibility(r, restored), Compatibility{Comparable: true}) {
		t.Fatalf("JSON-decoded report unexpectedly incompatible: %+v", InspectCompatibility(r, restored))
	}
}
