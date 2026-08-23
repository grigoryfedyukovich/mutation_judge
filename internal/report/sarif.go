package report

import (
	"encoding/json"
	"io"
	"sort"

	"github.com/example/mutation-judge/internal/model"
)

// SARIF result inclusion policy: only verdicts that are themselves
// evidence of a possible gap are included. KILLED means the test suite
// caught this mutant -- nothing to flag. INVALID and UNSUPPORTED are
// properties of the mutant or backend, not evidence about test
// quality (see docs/semantics.md), so they're not code-scanning
// findings either. SURVIVED, TIMEOUT, and UNKNOWN are the three
// verdicts docs/semantics.md itself describes as "we don't have
// positive evidence this is tested" -- those are what SARIF reports.
func sarifIncluded(v model.Verdict) (level string, ok bool) {
	switch v {
	case model.VerdictSurvived:
		return "warning", true
	case model.VerdictTimeout, model.VerdictUnknown:
		return "note", true
	default:
		return "", false
	}
}

type sarifLog struct {
	Schema  string     `json:"$schema"`
	Version string     `json:"version"`
	Runs    []sarifRun `json:"runs"`
}

type sarifRun struct {
	Tool       sarifTool      `json:"tool"`
	Results    []sarifResult  `json:"results"`
	Properties map[string]any `json:"properties"`
}

type sarifTool struct {
	Driver sarifDriver `json:"driver"`
}

type sarifDriver struct {
	Name    string      `json:"name"`
	Version string      `json:"version"`
	Rules   []sarifRule `json:"rules"`
}

type sarifRule struct {
	ID               string                 `json:"id"`
	Name             string                 `json:"name"`
	ShortDescription sarifText              `json:"shortDescription"`
	Properties       map[string]interface{} `json:"properties,omitempty"`
}

type sarifText struct {
	Text string `json:"text"`
}

type sarifResult struct {
	RuleID              string                 `json:"ruleId"`
	Level               string                 `json:"level"`
	Message             sarifText              `json:"message"`
	Locations           []sarifLocation        `json:"locations"`
	PartialFingerprints map[string]string      `json:"partialFingerprints,omitempty"`
	Properties          map[string]interface{} `json:"properties,omitempty"`
}

type sarifLocation struct {
	PhysicalLocation sarifPhysicalLocation `json:"physicalLocation"`
}

type sarifPhysicalLocation struct {
	ArtifactLocation sarifArtifactLocation `json:"artifactLocation"`
	Region           sarifRegion           `json:"region"`
}

type sarifArtifactLocation struct {
	URI string `json:"uri"`
}

type sarifRegion struct {
	StartLine   int `json:"startLine"`
	StartColumn int `json:"startColumn,omitempty"`
	EndLine     int `json:"endLine,omitempty"`
	EndColumn   int `json:"endColumn,omitempty"`
}

func renderSARIF(w io.Writer, r model.Report) error {
	rulesByID := map[string]sarifRule{}
	var results []sarifResult

	for _, x := range r.Results {
		level, ok := sarifIncluded(x.Verdict)
		if !ok {
			continue
		}
		m := x.Mutation
		if _, seen := rulesByID[m.RuleID]; !seen {
			rulesByID[m.RuleID] = sarifRule{
				ID:               m.RuleID,
				Name:             m.RuleID,
				ShortDescription: sarifText{Text: m.Description},
				Properties:       map[string]interface{}{"operator": m.Operator},
			}
		}
		props := map[string]interface{}{
			"verdict":        string(x.Verdict),
			"coverage_known": x.CoverageKnown,
			"covered":        x.Covered,
		}
		if len(x.Responsible) > 0 {
			props["responsible_tests"] = x.Responsible
		}
		results = append(results, sarifResult{
			RuleID:  m.RuleID,
			Level:   level,
			Message: sarifText{Text: findingMessage(x.Verdict, m.Description, m.Suggestion)},
			Locations: []sarifLocation{{PhysicalLocation: sarifPhysicalLocation{
				ArtifactLocation: sarifArtifactLocation{URI: m.Span.File},
				Region: sarifRegion{
					StartLine:   m.Span.StartLine,
					StartColumn: m.Span.StartCol,
					EndLine:     m.Span.EndLine,
					EndColumn:   m.Span.EndCol,
				},
			}}},
			// Mutant IDs are already content-addressed (same source,
			// same operator set, same mutant -> same ID across runs),
			// so this gives GitHub's code scanning a stable identity to
			// track one alert across commits instead of treating every
			// run's results as entirely new.
			PartialFingerprints: map[string]string{"mutationJudgeMutantId/v1": m.ID},
			Properties:          props,
		})
	}
	if results == nil {
		results = []sarifResult{}
	}

	rules := make([]sarifRule, 0, len(rulesByID))
	for _, id := range sortedKeys(rulesByID) {
		rules = append(rules, rulesByID[id])
	}

	log := sarifLog{
		// Verified against the live schema before shipping this: see
		// internal/report/sarif_test.go's schema-conformance test. The
		// older "master/Schemata/..." path GitHub's own SARIF docs
		// still cite as of this writing 404s; this is the path that
		// currently resolves.
		Schema:  "https://raw.githubusercontent.com/oasis-tcs/sarif-spec/main/sarif-2.1/schema/sarif-schema-2.1.0.json",
		Version: "2.1.0",
		Runs: []sarifRun{{
			Tool:    sarifTool{Driver: sarifDriver{Name: "mutation-judge", Version: r.ToolVersion, Rules: rules}},
			Results: results,
			Properties: map[string]any{
				"schema_version": r.SchemaVersion,
				"generated_at":   r.GeneratedAt,
				"complete":       r.Complete,
				"summary":        r.Summary,
				// RenderMeasured patches these two sentinels into the
				// serialized output after timing the render itself; see
				// report.go. They live here, in the run's properties
				// bag, rather than being invented fields on the SARIF
				// schema proper.
				"rendering_ms": r.Timing.RenderingMS,
				"total_ms":     r.Timing.TotalMS,
			},
		}},
	}

	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(log)
}

func sortedKeys(m map[string]sarifRule) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// findingMessage builds the shared human-readable text used by both
// the sarif and github formats for a single finding.
func findingMessage(v model.Verdict, description, suggestion string) string {
	switch v {
	case model.VerdictSurvived:
		msg := "mutation survived: " + description
		if suggestion != "" {
			msg += "\nsuggested test: " + suggestion
		}
		return msg
	case model.VerdictTimeout:
		return "mutation timed out (no verdict): " + description
	case model.VerdictUnknown:
		return "mutation could not be classified (no verdict): " + description
	default:
		return description
	}
}
