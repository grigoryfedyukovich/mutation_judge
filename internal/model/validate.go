package model

import (
	"encoding/json"
	"fmt"
	"io"
	"math"
	"strconv"
	"strings"
)

// DecodeReport validates imported JSON reports. The analyzer's in-memory
// reports are not subjected to these checks: this boundary protects compare
// and record from silently accepting missing, unrelated or corrupt evidence.
func DecodeReport(reader io.Reader) (Report, error) {
	d := json.NewDecoder(reader)
	var root map[string]json.RawMessage
	if err := d.Decode(&root); err != nil {
		return Report{}, fmt.Errorf("decode report: %w", err)
	}
	if root == nil {
		return Report{}, fmt.Errorf("report must be a JSON object")
	}
	var extra any
	if err := d.Decode(&extra); err != io.EOF {
		if err == nil {
			return Report{}, fmt.Errorf("unexpected JSON after report")
		}
		return Report{}, fmt.Errorf("trailing data after report: %w", err)
	}
	if err := requireFields(root, "report", "schema_version"); err != nil {
		return Report{}, err
	}
	var version string
	if err := json.Unmarshal(root["schema_version"], &version); err != nil {
		return Report{}, fmt.Errorf("schema_version: %w", err)
	}
	if err := ValidateSchemaVersion(version); err != nil {
		return Report{}, err
	}
	if err := requireFields(root, "report", "schema_version", "tool_version", "go_version", "generated_at", "complete", "patterns", "effective_config", "bounds", "semantics", "summary", "results", "timing"); err != nil {
		return Report{}, err
	}
	if err := validateStructure(root); err != nil {
		return Report{}, err
	}
	// JSON-decoded maps normally use float64 for numbers. Decode the three
	// discovery counts exactly and retain them as ints for history/compare.
	bounds, err := exactBounds(root["bounds"])
	if err != nil {
		return Report{}, err
	}
	raw, err := json.Marshal(root)
	if err != nil {
		return Report{}, fmt.Errorf("marshal report fields: %w", err)
	}
	var r Report
	if err := json.Unmarshal(raw, &r); err != nil {
		return Report{}, fmt.Errorf("decode report fields: %w", err)
	}
	for k, n := range bounds {
		r.Bounds[k] = n
	}
	if err := ValidateReport(r); err != nil {
		return Report{}, err
	}
	return r, nil
}

// Nested presence checks avoid silently substituting Go zero values for
// required fields. Empty strings for a mutation's replacement are legitimate.
func requireFields(m map[string]json.RawMessage, scope string, keys ...string) error {
	for _, key := range keys {
		value, ok := m[key]
		if !ok || len(value) == 0 {
			return fmt.Errorf("%s: missing required field %s", scope, key)
		}
	}
	return nil
}

func jsonObject(raw json.RawMessage, scope string) (map[string]json.RawMessage, error) {
	var m map[string]json.RawMessage
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, fmt.Errorf("%s: %w", scope, err)
	}
	if m == nil {
		return nil, fmt.Errorf("%s must be an object", scope)
	}
	return m, nil
}

// exactBounds ensures counts are JSON integers (not fractions or numbers that
// lose precision as float64). The decoded report keeps exact integer values.
func exactBounds(raw json.RawMessage) (map[string]int, error) {
	fields, err := jsonObject(raw, "bounds")
	if err != nil {
		return nil, err
	}
	counts := make(map[string]int, 3)
	for _, key := range []string{"discovered_before_bound", "retained_after_bound", "completed_mutants"} {
		n, err := strconv.ParseInt(string(fields[key]), 10, strconv.IntSize)
		if err != nil || n < 0 {
			return nil, fmt.Errorf("bounds.%s must be a nonnegative integer", key)
		}
		counts[key] = int(n)
	}
	return counts, nil
}

func validateStructure(root map[string]json.RawMessage) error {
	summary, err := jsonObject(root["summary"], "summary")
	if err != nil {
		return err
	}
	if err := requireFields(summary, "summary", "generated", "killed", "survived", "invalid", "timeout", "unknown", "unsupported", "equivalent", "score", "score_text"); err != nil {
		return err
	}
	if _, err := jsonObject(root["bounds"], "bounds"); err != nil {
		return err
	}
	if _, err := jsonObject(root["effective_config"], "effective_config"); err != nil {
		return err
	}
	timing, err := jsonObject(root["timing"], "timing")
	if err != nil {
		return err
	}
	if err := requireFields(timing, "timing", "parsing_ms", "baseline_ms", "execution_ms", "rendering_ms", "total_ms"); err != nil {
		return err
	}
	// Some old valid empty reports encode results as null; treat that as []
	// only when summary and bounds agree on zero, never as an omitted field.
	if string(root["results"]) == "null" {
		return nil
	}
	var results []json.RawMessage
	if err := json.Unmarshal(root["results"], &results); err != nil {
		return fmt.Errorf("results: %w", err)
	}
	for i, raw := range results {
		loc := fmt.Sprintf("results[%d]", i)
		result, err := jsonObject(raw, loc)
		if err != nil {
			return err
		}
		if err := requireFields(result, loc, "mutation", "verdict", "diagnostic"); err != nil {
			return err
		}
		mut, err := jsonObject(result["mutation"], loc+".mutation")
		if err != nil {
			return err
		}
		if err := requireFields(mut, loc+".mutation", "id", "operator", "rule_id", "span", "original", "replacement", "description", "suggestion", "diff"); err != nil {
			return err
		}
		span, err := jsonObject(mut["span"], loc+".mutation.span")
		if err != nil {
			return err
		}
		if err := requireFields(span, loc+".mutation.span", "file", "start_byte", "end_byte", "start_line", "start_column", "end_line", "end_column"); err != nil {
			return err
		}
		diag, err := jsonObject(result["diagnostic"], loc+".diagnostic")
		if err != nil {
			return err
		}
		if err := requireFields(diag, loc+".diagnostic", "id", "location", "statement"); err != nil {
			return err
		}
	}
	return nil
}

func ValidateReport(r Report) error {
	if err := ValidateSchemaVersion(r.SchemaVersion); err != nil {
		return err
	}
	if strings.TrimSpace(r.ToolVersion) == "" || strings.TrimSpace(r.GoVersion) == "" {
		return fmt.Errorf("tool_version and go_version must be nonempty")
	}
	if r.GeneratedAt.IsZero() {
		return fmt.Errorf("generated_at must be a valid, nonzero timestamp")
	}
	if len(r.Patterns) == 0 {
		return fmt.Errorf("patterns must identify a test scope")
	}
	for _, p := range r.Patterns {
		if strings.TrimSpace(p) == "" {
			return fmt.Errorf("patterns must not contain empty entries")
		}
	}
	if r.EffectiveConfig == nil || r.Bounds == nil || r.Semantics == nil {
		return fmt.Errorf("missing effective_config, bounds or semantics")
	}
	if len(r.EffectiveConfig) == 0 || len(r.Semantics) == 0 {
		return fmt.Errorf("effective_config and semantics must be nonempty")
	}
	if r.Timing.ParsingMS < 0 || r.Timing.BaselineMS < 0 || r.Timing.ExecutionMS < 0 || r.Timing.RenderingMS < 0 || r.Timing.TotalMS < 0 {
		return fmt.Errorf("negative timing value")
	}
	bounds := make([]int, 3)
	for i, key := range []string{"discovered_before_bound", "retained_after_bound", "completed_mutants"} {
		n, ok := nonnegativeInteger(r.Bounds[key])
		if !ok {
			return fmt.Errorf("bounds.%s must be a nonnegative integer", key)
		}
		bounds[i] = n
	}
	if bounds[0] < bounds[1] || bounds[1] < bounds[2] || bounds[2] != len(r.Results) {
		return fmt.Errorf("inconsistent bounds: discovered=%d retained=%d completed=%d results=%d", bounds[0], bounds[1], bounds[2], len(r.Results))
	}
	if r.Complete && bounds[1] != bounds[2] {
		return fmt.Errorf("complete report has %d of %d retained mutants", bounds[2], bounds[1])
	}
	counts := map[Verdict]int{}
	ids := make(map[string]struct{}, len(r.Results))
	for i, result := range r.Results {
		if result.Mutation.ID == "" {
			return fmt.Errorf("results[%d]: empty mutant ID", i)
		}
		if _, found := ids[result.Mutation.ID]; found {
			return fmt.Errorf("results[%d]: duplicate mutant ID %q", i, result.Mutation.ID)
		}
		ids[result.Mutation.ID] = struct{}{}
		switch result.Verdict {
		case VerdictKilled, VerdictSurvived, VerdictInvalid, VerdictTimeout, VerdictUnknown, VerdictUnsupported, VerdictEquivalent:
		default:
			return fmt.Errorf("results[%d]: invalid verdict %q", i, result.Verdict)
		}
		counts[result.Verdict]++
		span := result.Mutation.Span
		if span.File == "" || span.StartByte < 0 || span.EndByte <= span.StartByte || span.StartLine < 1 || span.EndLine < span.StartLine || span.StartCol < 1 || span.EndCol < 1 {
			return fmt.Errorf("results[%d]: invalid mutation source span", i)
		}
		if result.Mutation.Operator == "" || result.Mutation.RuleID == "" {
			return fmt.Errorf("results[%d]: missing operator or rule_id", i)
		}
		if result.Diagnostic.ID == "" || result.Diagnostic.Statement == "" {
			return fmt.Errorf("results[%d]: missing diagnostic identifier or statement", i)
		}
		if result.DurationMS < 0 {
			return fmt.Errorf("results[%d]: negative duration", i)
		}
	}
	s := r.Summary
	if s.Generated != len(r.Results) || s.Killed != counts[VerdictKilled] || s.Survived != counts[VerdictSurvived] || s.Invalid != counts[VerdictInvalid] || s.Timeout != counts[VerdictTimeout] || s.Unknown != counts[VerdictUnknown] || s.Unsupported != counts[VerdictUnsupported] || s.Equivalent != counts[VerdictEquivalent] {
		return fmt.Errorf("summary counts disagree with results")
	}
	den := s.Killed + s.Survived
	var score float64
	text := "n/a (no scoreable mutants)"
	if den > 0 {
		score = 100 * float64(s.Killed) / float64(den)
		text = fmt.Sprintf("%.1f%% excluding invalid/timeout/unknown/unsupported/equivalent", score)
	}
	if math.IsNaN(s.Score) || math.IsInf(s.Score, 0) || math.Abs(s.Score-score) > 1e-8 || s.ScoreText != text {
		return fmt.Errorf("summary score or score_text disagrees with verdict counts")
	}
	return nil
}

func nonnegativeInteger(v any) (int, bool) {
	switch x := v.(type) {
	case int:
		return x, x >= 0
	case float64:
		if x >= 0 && !math.IsInf(x, 0) && x < math.Exp2(float64(strconv.IntSize-1)) && math.Trunc(x) == x {
			return int(x), true
		}
	case json.Number:
		n, err := strconv.ParseInt(string(x), 10, 64)
		if err == nil && n >= 0 && int64(int(n)) == n {
			return int(n), true
		}
	}
	return 0, false
}
