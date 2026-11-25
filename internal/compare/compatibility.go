package compare

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strings"

	"github.com/example/mutation-judge/internal/model"
)

// Compatibility identifies mismatched experiment settings and missing evidence.
// Different source code is expected across revisions; a different test universe,
// instrumentation, language/tool semantics, or unfinished discovery is not.
type Compatibility struct {
	Comparable bool     `json:"comparable"`
	Issues     []string `json:"issues,omitempty"`
}

func InspectCompatibility(baseline, current model.Report) Compatibility {
	var issues []string
	add := func(format string, args ...any) { issues = append(issues, fmt.Sprintf(format, args...)) }
	for _, side := range []struct {
		name string
		r    model.Report
	}{{"baseline", baseline}, {"current", current}} {
		if changed, ok := side.r.EffectiveConfig["changed"].(string); ok && changed != "" {
			add("%s report uses incremental --changed filtering; absence does not prove site removal", side.name)
		}
		if complete, ok := side.r.Bounds["execution_inputs_complete"].(bool); !ok || !complete {
			add("%s report cannot fingerprint every execution dependency (external local replacements, flag files, or missing provenance)", side.name)
		}
		if !side.r.Complete {
			add("%s report is incomplete", side.name)
		}
		if side.r.Bounds == nil {
			add("%s report has no discovery/command provenance", side.name)
			continue
		}
		discovered, dok := integralBound(side.r.Bounds, "discovered_before_bound")
		retained, rok := integralBound(side.r.Bounds, "retained_after_bound")
		completed, cok := integralBound(side.r.Bounds, "completed_mutants")
		if !dok || !rok || !cok {
			add("%s report lacks discovery/completion counts", side.name)
		} else {
			if discovered < retained || retained < completed || completed != len(side.r.Results) {
				add("%s report has inconsistent discovery/completion counts", side.name)
			}
			if discovered > retained {
				add("%s report selected only %d of %d discovered mutants", side.name, retained, discovered)
			}
			if completed < retained {
				add("%s report analysed only %d of %d retained mutants", side.name, completed, retained)
			}
		}
		if len(side.r.Patterns) == 0 {
			add("%s report lacks test pattern scope", side.name)
		}
	}
	if !reflect.DeepEqual(baseline.Patterns, current.Patterns) {
		add("test patterns differ")
	}
	// Only inputs which change test or mutation semantics matter; rendering,
	// cache/worker and scoring policy do not affect a given mutant verdict.
	configKeys := []string{"operators", "test_run", "timeout", "include_generated", "changed", "narrow_test_scope", "coverage_test_selection"}
	boundKeys := []string{"backend_name", "backend_version", "operator_semantics", "test_working_directory", "test_command", "resolved_test_packages", "goos", "goarch", "cgo_enabled", "goflags", "execution_environment_hash", "effective_go_config_hash", "execution_inputs_complete", "per_mutant_timeout"}
	compareFields := func(label string, b, c map[string]any, keys []string) {
		for _, key := range keys {
			bv, bok := b[key]
			cv, cok := c[key]
			if !bok || !cok {
				add("missing %s.%s", label, key)
			} else if !equalJSON(bv, cv) {
				add("%s.%s differs", label, key)
			}
		}
	}
	compareFields("effective_config", baseline.EffectiveConfig, current.EffectiveConfig, configKeys)
	compareFields("bounds", baseline.Bounds, current.Bounds, boundKeys)
	if baseline.GoVersion == "" || current.GoVersion == "" {
		add("missing Go version")
	} else if baseline.GoVersion != current.GoVersion {
		add("Go version differs")
	}
	if baseline.ToolVersion == "" || current.ToolVersion == "" {
		add("missing tool version")
	} else if baseline.ToolVersion != current.ToolVersion {
		add("tool version differs")
	}
	// Effective -run selection may change across source revisions even when
	// the top-level options match. A verdict comparison is only supported if
	// its actual per-mutant commands agree across shared IDs.
	byID := make(map[string]model.Result, len(baseline.Results))
	for _, r := range baseline.Results {
		byID[r.Mutation.ID] = r
	}
	for _, r := range current.Results {
		old, ok := byID[r.Mutation.ID]
		if !ok || (old.Verdict == model.VerdictEquivalent && r.Verdict == model.VerdictEquivalent) {
			continue
		}
		a := old.Diagnostic.Evidence["test_command"]
		b := r.Diagnostic.Evidence["test_command"]
		if a == nil || b == nil {
			add("missing actual per-mutant test command for ID %s", r.Mutation.ID)
			break
		}
		if !equalJSON(a, b) {
			add("actual test command differs for mutant %s", r.Mutation.ID)
			break
		}
	}
	// max_mutants is not a comparable setting if it actually truncates either
	// population. Different harmless max values are fine when both discover all.
	return Compatibility{Comparable: len(issues) == 0, Issues: issues}
}

func integralBound(bounds map[string]any, key string) (int, bool) {
	v, ok := bounds[key]
	if !ok {
		return 0, false
	}
	switch n := v.(type) {
	case int:
		return n, n >= 0
	case float64:
		if n >= 0 && n == float64(int(n)) {
			return int(n), true
		}
	case json.Number:
		x, err := n.Int64()
		if err == nil && x >= 0 && int64(int(x)) == x {
			return int(x), true
		}
	}
	return 0, false
}

func equalJSON(a, b any) bool {
	x, ex := json.Marshal(a)
	y, ey := json.Marshal(b)
	return ex == nil && ey == nil && string(x) == string(y)
}

// CompareReports is the report-aware variant for all user-facing comparisons.
// It distinguishes an ID missing from an incomplete or incomparable experiment
// from an actually removed/new site. Its output is advisory only when
// Comparable is false; the CLI rejects those reports unless explicitly overridden.
func CompareReports(baseline, current model.Report) Diff {
	d := Compare(baseline, current)
	c := InspectCompatibility(baseline, current)
	d.Comparable = c.Comparable
	d.CompatibilityIssues = c.Issues
	if c.Comparable {
		return d
	}
	// Missing IDs cannot prove either addition or removal if either run has
	// incomplete coverage of its requested mutant universe, or a different
	// package/test population. Never classify an absence as a code change.
	d.NotAnalyzed = append(d.NotAnalyzed, d.RemovedMutants...)
	d.RemovedMutants = nil
	for _, md := range d.NewSurvivors {
		if md.Baseline == nil {
			d.NotAnalyzed = append(d.NotAnalyzed, md)
		} else {
			d.UnverifiedChanges = append(d.UnverifiedChanges, md)
		}
	}
	d.NewSurvivors = nil
	d.UnverifiedChanges = append(d.UnverifiedChanges, d.FixedSurvivors...)
	d.UnverifiedChanges = append(d.UnverifiedChanges, d.StillOpen...)
	d.UnverifiedChanges = append(d.UnverifiedChanges, d.Reclassified...)
	d.FixedSurvivors = nil
	d.StillOpen = nil
	d.Reclassified = nil
	sortByLocation(d.NotAnalyzed)
	sortByLocation(d.UnverifiedChanges)
	// Shift correlations relied on one side being genuinely removed/new.
	d.LikelyShifted = nil
	return d
}

func CompatibilityError(c Compatibility) error {
	if c.Comparable {
		return nil
	}
	return fmt.Errorf("reports are not comparable: %s (pass --allow-incompatible for an explicitly exploratory diff)", strings.Join(c.Issues, "; "))
}
