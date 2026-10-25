package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/example/mutation-judge/internal/model"
)

func TestF11CompareAndRecordValidateImportedReport(t *testing.T) {
	dir := t.TempDir()
	mutation := model.Mutation{
		ID: "M-test", Operator: "boundary", RuleID: "test", Original: ">", Replacement: ">=",
		Span: model.Span{File: "f.go", StartByte: 10, EndByte: 11, StartLine: 2, EndLine: 2, StartCol: 1, EndCol: 2},
	}
	r := model.Report{
		SchemaVersion: model.SchemaVersion, ToolVersion: "0.1.3", GoVersion: "go1.23", GeneratedAt: time.Now().UTC(), Complete: true,
		Patterns: []string{"./..."}, EffectiveConfig: map[string]any{"operators": []string{"boundary"}, "test_run": "", "timeout": "30s", "include_generated": false, "changed": "", "narrow_test_scope": false, "coverage_test_selection": false},
		Bounds: map[string]any{
			"discovered_before_bound": 1, "retained_after_bound": 1, "completed_mutants": 1,
			"execution_inputs_complete": true, "backend_name": "go-test", "backend_version": "v1", "operator_semantics": "v15", "test_working_directory": ".", "test_command": "go test ./...", "resolved_test_packages": []string{"example.test/a"}, "goos": "linux", "goarch": "amd64", "cgo_enabled": "1", "goflags": "", "execution_environment_hash": "hash", "effective_go_config_hash": "go-config-hash", "per_mutant_timeout": "30s",
		},
		Semantics: []string{"independent"},
		Summary:   model.Summary{Generated: 1, Killed: 1, Score: 100, ScoreText: "100.0% excluding invalid/timeout/unknown/unsupported/equivalent"},
		Results:   []model.Result{{Mutation: mutation, Verdict: model.VerdictKilled, Diagnostic: model.Diagnostic{ID: "MJ-VERDICT-KILLED", Statement: "killed", Evidence: map[string]any{"test_command": "go test ./..."}}}},
	}
	path := filepath.Join(dir, "report.json")
	historyPath := filepath.Join(dir, "history.ndjson")
	write := func(data []byte) {
		t.Helper()
		if err := os.WriteFile(path, data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	encode := func() []byte {
		t.Helper()
		b, err := json.Marshal(r)
		if err != nil {
			t.Fatal(err)
		}
		return b
	}
	write(encode())
	if code := runRecord([]string{"--label", "good", "--history-file", historyPath, path}); code != exitOK {
		t.Fatalf("valid report rejected by record: %d", code)
	}
	if code := runCompare([]string{"--baseline", path, "--current", path, "--format", "json", "--output", filepath.Join(dir, "comparison.json")}); code != exitOK {
		t.Fatalf("valid report rejected by compare: %d", code)
	}

	// Comparison override must never bypass malformed input validation.
	r.Results[0].Verdict = "GUESSED"
	write(encode())
	if code := runCompare([]string{"--baseline", path, "--current", path, "--allow-incompatible"}); code != exitInput {
		t.Fatalf("compare accepted invalid verdict: %d", code)
	}
	if code := runRecord([]string{"--label", "bad", "--history-file", historyPath, path}); code != exitInput {
		t.Fatalf("record accepted invalid verdict: %d", code)
	}
	data, err := os.ReadFile(historyPath)
	if err != nil {
		t.Fatal(err)
	}
	if len(data) == 0 || !json.Valid(data) {
		t.Fatalf("history changed or corrupt after rejection: %q", data)
	}
	var entry struct {
		Discovered int `json:"discovered_before_bound"`
		Retained   int `json:"retained_after_bound"`
		Completed  int `json:"completed_mutants"`
	}
	if err := json.Unmarshal(data, &entry); err != nil {
		t.Fatal(err)
	}
	if entry.Discovered != 1 || entry.Retained != 1 || entry.Completed != 1 {
		t.Fatalf("record lost imported discovery provenance: %+v", entry)
	}
}
