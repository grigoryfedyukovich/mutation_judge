package model

import (
	"fmt"
	"strings"
	"time"
)

const SchemaVersion = "mutation-judge.report/v1"

// ValidateSchemaVersion checks that got's schema major (the "v1" in
// "mutation-judge.report/v1") matches this build's own SchemaVersion,
// returning a descriptive error if it doesn't -- including when got is
// empty (an empty JSON object, a completely different file format that
// still happens to decode as valid but unrelated JSON, or any other
// input with no schema_version field at all) or otherwise unparseable.
//
// This package's own documented policy (README.md, docs/tutorial.md)
// tells external consumers of this JSON to reject an unknown schema
// major rather than silently assume field semantics that may have
// changed under it. This tool's own consumers of its own JSON output
// -- `mutation-judge compare` and `mutation-judge record`, both of
// which read a previously-written report back in as model.Report --
// are not exempt from that same policy: encoding/json silently leaves
// every field at its zero value for anything present in the input but
// absent from (or differently shaped than) this struct, so decoding a
// wrong-major-version report, or any other JSON object that isn't
// actually a mutation-judge report, previously succeeded outright and
// produced an all-zero-value Report -- an empty diff or a zero score,
// with no error at all telling the caller why. See ISSUES.md.
func ValidateSchemaVersion(got string) error {
	if got == "" {
		return fmt.Errorf("missing schema_version (not a mutation-judge report, or an empty/incompatible file); this build understands %s", SchemaVersion)
	}
	wantMajor, gotMajor := schemaMajor(SchemaVersion), schemaMajor(got)
	if gotMajor == "" || gotMajor != wantMajor {
		return fmt.Errorf("unsupported schema_version %q; this build understands %s", got, SchemaVersion)
	}
	return nil
}

// schemaMajor extracts the component after the last "/" in a schema
// string like "mutation-judge.report/v1" ("v1"), or "" if there is no
// "/" or nothing follows it.
func schemaMajor(v string) string {
	i := strings.LastIndex(v, "/")
	if i < 0 || i == len(v)-1 {
		return ""
	}
	return v[i+1:]
}

type Span struct {
	File      string `json:"file"`
	StartByte int    `json:"start_byte"`
	EndByte   int    `json:"end_byte"`
	StartLine int    `json:"start_line"`
	StartCol  int    `json:"start_column"`
	EndLine   int    `json:"end_line"`
	EndCol    int    `json:"end_column"`
}

type Mutation struct {
	ID          string `json:"id"`
	Operator    string `json:"operator"`
	RuleID      string `json:"rule_id"`
	Span        Span   `json:"span"`
	Original    string `json:"original"`
	Replacement string `json:"replacement"`
	Description string `json:"description"`
	Suggestion  string `json:"suggestion"`
	Diff        string `json:"diff"`
	// EquivalentReason is non-empty only when discovery itself proved
	// this specific mutant is behaviorally equivalent to the original
	// program in every reachable state -- currently just the boundary
	// operator's guarded-comparison case (see
	// internal/frontend.detectGuardedComparison). When set, the mutant
	// is never executed at all: see model.VerdictEquivalent.
	EquivalentReason string `json:"equivalent_reason,omitempty"`
}

type Verdict string

const (
	VerdictKilled      Verdict = "KILLED"
	VerdictSurvived    Verdict = "SURVIVED"
	VerdictInvalid     Verdict = "INVALID"
	VerdictTimeout     Verdict = "TIMEOUT"
	VerdictUnknown     Verdict = "UNKNOWN"
	VerdictUnsupported Verdict = "UNSUPPORTED"
	// VerdictEquivalent means discovery itself proved this mutant
	// behaviorally equivalent to the original before any test ever
	// ran -- see Mutation.EquivalentReason. Like INVALID, TIMEOUT,
	// UNKNOWN, and UNSUPPORTED, it is excluded from the score
	// denominator: it is neither a demonstrated test gap (SURVIVED)
	// nor demonstrated test strength (KILLED), and asserting it as
	// either would be a claim discovery has no basis for.
	VerdictEquivalent Verdict = "EQUIVALENT"
)

type Timing struct {
	ParsingMS   int64 `json:"parsing_ms"`
	BaselineMS  int64 `json:"baseline_ms"`
	ExecutionMS int64 `json:"execution_ms"`
	RenderingMS int64 `json:"rendering_ms"`
	TotalMS     int64 `json:"total_ms"`
}

type Diagnostic struct {
	ID          string         `json:"id"`
	Location    Span           `json:"location"`
	Statement   string         `json:"statement"`
	Evidence    map[string]any `json:"evidence,omitempty"`
	Assumptions []string       `json:"assumptions,omitempty"`
	Suggestion  string         `json:"suggestion,omitempty"`
}

type Result struct {
	Mutation      Mutation   `json:"mutation"`
	Verdict       Verdict    `json:"verdict"`
	Responsible   []string   `json:"responsible_tests,omitempty"`
	Covered       bool       `json:"covered_by_selected_tests"`
	CoverageKnown bool       `json:"coverage_known"`
	Cached        bool       `json:"cached"`
	DurationMS    int64      `json:"duration_ms"`
	Output        string     `json:"output,omitempty"`
	Diagnostic    Diagnostic `json:"diagnostic"`
}

type Summary struct {
	Generated   int     `json:"generated"`
	Killed      int     `json:"killed"`
	Survived    int     `json:"survived"`
	Invalid     int     `json:"invalid"`
	Timeout     int     `json:"timeout"`
	Unknown     int     `json:"unknown"`
	Unsupported int     `json:"unsupported"`
	Equivalent  int     `json:"equivalent"`
	Score       float64 `json:"score"`
	ScoreText   string  `json:"score_text"`
}

type Report struct {
	SchemaVersion   string         `json:"schema_version"`
	ToolVersion     string         `json:"tool_version"`
	GoVersion       string         `json:"go_version"`
	GeneratedAt     time.Time      `json:"generated_at"`
	Complete        bool           `json:"complete"`
	Patterns        []string       `json:"patterns"`
	EffectiveConfig map[string]any `json:"effective_config"`
	Bounds          map[string]any `json:"bounds"`
	Semantics       []string       `json:"semantics"`
	Warnings        []string       `json:"warnings,omitempty"`
	Summary         Summary        `json:"summary"`
	Results         []Result       `json:"results"`
	Timing          Timing         `json:"timing"`
}
