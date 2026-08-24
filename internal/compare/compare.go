// Package compare diffs two Mutation Judge reports at the mutant
// level, matching mutants by their stable ID (see
// internal/frontend.mutationID) to answer "did this change introduce
// new test gaps, or fix existing ones?" across two runs -- typically a
// base branch's report and a pull request's report.
//
// Matching is ID-exact, which has a known limitation worth stating up
// front: a mutant's ID is a hash of its file path, its byte offset in
// that file, and its own text -- not an AST-stable identity. An edit
// anywhere earlier in the same file shifts the byte offset of every
// mutant after it, changing its ID even when that mutation site itself
// is untouched. A file the change never touches at all compares
// reliably; a file the change edits will show ID churn below the edit
// point regardless of whether those lower mutation sites actually
// changed. See docs/limitations.md.
package compare

import (
	"sort"

	"github.com/example/mutation-judge/internal/model"
)

// Actionable mirrors the same "not positively confirmed as tested"
// verdict set the sarif and github report formats use: SURVIVED,
// TIMEOUT, and UNKNOWN are actionable; KILLED, INVALID, and
// UNSUPPORTED are not. See docs/semantics.md.
func Actionable(v model.Verdict) bool {
	switch v {
	case model.VerdictSurvived, model.VerdictTimeout, model.VerdictUnknown:
		return true
	default:
		return false
	}
}

// MutantDiff describes one mutant ID's actionable-status change.
// Baseline and/or Current is nil when the mutant is absent from that
// side (a mutant that no longer exists, or one introduced by new
// code) rather than merely re-classified.
type MutantDiff struct {
	ID       string        `json:"id"`
	Baseline *model.Result `json:"baseline,omitempty"`
	Current  *model.Result `json:"current,omitempty"`
}

// Mutation returns the mutant's own data from whichever side is
// present; when both are present they describe the same content by
// construction (same ID = same file, offset, operator, rule, original,
// and replacement text), so either is equally valid to display.
func (d MutantDiff) Mutation() model.Mutation {
	if d.Current != nil {
		return d.Current.Mutation
	}
	return d.Baseline.Mutation
}

// Diff is the result of comparing two reports.
type Diff struct {
	// NewSurvivors are mutant IDs actionable in Current but not in
	// Baseline: either a previously-killed mutant regressed, or a
	// brand-new mutant (from new code) was actionable from the start.
	// These are the entries worth a reviewer's attention.
	NewSurvivors []MutantDiff `json:"new_survivors"`
	// FixedSurvivors are the reverse: actionable in Baseline but not
	// in Current -- a mutant that got killed by improved tests, or
	// that no longer exists at all (its code was removed or
	// refactored away).
	FixedSurvivors []MutantDiff `json:"fixed_survivors"`
	// UnchangedCount is every other mutant ID appearing in either
	// report: present in both sides with the same actionable-status,
	// so there's nothing to report about it. NewSurvivors,
	// FixedSurvivors, and UnchangedCount always sum to the total
	// number of distinct mutant IDs across both reports.
	UnchangedCount int     `json:"unchanged_count"`
	BaselineScore  float64 `json:"baseline_score"`
	CurrentScore   float64 `json:"current_score"`
	BaselineText   string  `json:"baseline_score_text"`
	CurrentText    string  `json:"current_score_text"`
}

// Compare builds a Diff between a baseline report (e.g. the base
// branch) and a current report (e.g. a pull request), matched by
// mutant ID.
func Compare(baseline, current model.Report) Diff {
	baseByID := map[string]model.Result{}
	for _, r := range baseline.Results {
		baseByID[r.Mutation.ID] = r
	}
	curByID := map[string]model.Result{}
	for _, r := range current.Results {
		curByID[r.Mutation.ID] = r
	}

	d := Diff{
		BaselineScore: baseline.Summary.Score, BaselineText: baseline.Summary.ScoreText,
		CurrentScore: current.Summary.Score, CurrentText: current.Summary.ScoreText,
	}

	seen := make(map[string]bool, len(baseByID)+len(curByID))
	consider := func(id string) {
		if seen[id] {
			return
		}
		seen[id] = true
		b, hasB := baseByID[id]
		c, hasC := curByID[id]
		bActionable := hasB && Actionable(b.Verdict)
		cActionable := hasC && Actionable(c.Verdict)
		switch {
		case cActionable && !bActionable:
			md := MutantDiff{ID: id, Current: resultPtr(c, hasC)}
			if hasB {
				md.Baseline = resultPtr(b, hasB)
			}
			d.NewSurvivors = append(d.NewSurvivors, md)
		case bActionable && !cActionable:
			md := MutantDiff{ID: id, Baseline: resultPtr(b, hasB)}
			if hasC {
				md.Current = resultPtr(c, hasC)
			}
			d.FixedSurvivors = append(d.FixedSurvivors, md)
		default:
			d.UnchangedCount++
		}
	}
	for _, r := range baseline.Results {
		consider(r.Mutation.ID)
	}
	for _, r := range current.Results {
		consider(r.Mutation.ID)
	}

	sortByLocation(d.NewSurvivors)
	sortByLocation(d.FixedSurvivors)
	return d
}

func resultPtr(r model.Result, ok bool) *model.Result {
	if !ok {
		return nil
	}
	return &r
}

func sortByLocation(list []MutantDiff) {
	sort.Slice(list, func(i, j int) bool {
		mi, mj := list[i].Mutation(), list[j].Mutation()
		if mi.Span.File != mj.Span.File {
			return mi.Span.File < mj.Span.File
		}
		if mi.Span.StartLine != mj.Span.StartLine {
			return mi.Span.StartLine < mj.Span.StartLine
		}
		if mi.Span.StartCol != mj.Span.StartCol {
			return mi.Span.StartCol < mj.Span.StartCol
		}
		return list[i].ID < list[j].ID
	})
}
