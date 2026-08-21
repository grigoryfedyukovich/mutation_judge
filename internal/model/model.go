package model

import "time"

const SchemaVersion = "mutation-judge.report/v1"

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
}

type Verdict string

const (
	VerdictKilled      Verdict = "KILLED"
	VerdictSurvived    Verdict = "SURVIVED"
	VerdictInvalid     Verdict = "INVALID"
	VerdictTimeout     Verdict = "TIMEOUT"
	VerdictUnknown     Verdict = "UNKNOWN"
	VerdictUnsupported Verdict = "UNSUPPORTED"
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
