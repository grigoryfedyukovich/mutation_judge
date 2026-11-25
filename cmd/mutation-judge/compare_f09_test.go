package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/example/mutation-judge/internal/model"
)

func TestF09CLIRejectsBoundedComparisonAndAllowsExploratoryMode(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "mutation-judge")
	build := exec.Command("go", "build", "-o", bin, ".")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build: %v: %s", err, out)
	}
	fixture := func(id string) model.Result {
		return model.Result{Mutation: model.Mutation{ID: id, Operator: "boundary", RuleID: "boundary-v1", Span: model.Span{File: "a.go", StartByte: 10, EndByte: 11, StartLine: 2, EndLine: 2, StartCol: 1, EndCol: 2}}, Verdict: model.VerdictSurvived, Diagnostic: model.Diagnostic{ID: "MJ-VERDICT-SURVIVED", Statement: "survived", Evidence: map[string]any{"test_command": "go test ./..."}}}
	}
	report := func(results ...model.Result) model.Report {
		return model.Report{SchemaVersion: model.SchemaVersion, ToolVersion: "test", GoVersion: "go1.22", GeneratedAt: time.Now().UTC(), Complete: true, Semantics: []string{"test semantics"}, Patterns: []string{"./..."},
			EffectiveConfig: map[string]any{"operators": []string{"boundary"}, "test_run": "", "timeout": "30s", "include_generated": false, "changed": "", "narrow_test_scope": false, "coverage_test_selection": false},
			Bounds:          map[string]any{"discovered_before_bound": len(results), "retained_after_bound": len(results), "completed_mutants": len(results), "backend_name": "go-test", "backend_version": "v1", "operator_semantics": "v15", "test_working_directory": ".", "test_command": "go test ./...", "resolved_test_packages": []string{"example.test/a"}, "goos": "linux", "goarch": "amd64", "cgo_enabled": "1", "goflags": "", "execution_environment_hash": "hash", "effective_go_config_hash": "go-config-hash", "execution_inputs_complete": true, "per_mutant_timeout": "30s"}, Results: results, Summary: model.Summary{Generated: len(results), Survived: len(results), ScoreText: "0.0% excluding invalid/timeout/unknown/unsupported/equivalent"}}
	}
	base := report(fixture("a"), fixture("b"))
	cur := report(fixture("a"))
	cur.Bounds["discovered_before_bound"] = 2
	basePath := filepath.Join(dir, "base.json")
	curPath := filepath.Join(dir, "current.json")
	for path, r := range map[string]model.Report{basePath: base, curPath: cur} {
		b, err := json.Marshal(r)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, b, 0600); err != nil {
			t.Fatal(err)
		}
	}
	args := []string{"compare", "--baseline", basePath, "--current", curPath, "--format", "json"}
	cmd := exec.Command(bin, args...)
	out, err := cmd.CombinedOutput()
	if err == nil || !strings.Contains(string(out), "not comparable") {
		t.Fatalf("default should reject: %v %s", err, out)
	}
	cmd = exec.Command(bin, append(args, "--allow-incompatible")...)
	out, err = cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("override should work: %v %s", err, out)
	}
	var d struct {
		Comparable  bool  `json:"comparable"`
		Removed     []any `json:"removed_mutants"`
		NotAnalyzed []any `json:"not_analyzed"`
	}
	if err := json.Unmarshal(out, &d); err != nil {
		t.Fatalf("bad output: %v %s", err, out)
	}
	if d.Comparable || len(d.Removed) != 0 || len(d.NotAnalyzed) != 1 {
		t.Fatalf("false removal: %+v", d)
	}
	cmd = exec.Command(bin, append(args, "--allow-incompatible", "--fail-on-new-survivors")...)
	out, err = cmd.CombinedOutput()
	if err == nil || !strings.Contains(string(out), "requires compatible") {
		t.Fatalf("gate should reject exploratory comparison: %v %s", err, out)
	}
}
