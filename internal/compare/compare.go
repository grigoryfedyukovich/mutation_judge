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
// changed -- concretely, the same survivor can disappear from
// RemovedMutants and reappear in NewSurvivors under a new ID, even
// though nothing about that comparison itself changed. See
// docs/limitations.md.
//
// findLikelyShifts (shift.go) narrows that specific failure mode: it
// looks for an unambiguous structural fingerprint match between a
// removed entry and a brand-new-ID entry in the same file and, when it
// finds exactly one, records the correlation in Diff.LikelyShifted.
// This is additive and heuristic, not a correction -- NewSurvivors,
// FixedSurvivors, StillOpen, Reclassified, RemovedMutants, and
// UnchangedCount are always the exact, ID-based truth, unchanged by
// whatever LikelyShifted finds.
package compare

import (
	"sort"

	"github.com/example/mutation-judge/internal/model"
)

// Actionable mirrors the same "not positively confirmed as tested"
// verdict set the sarif and github report formats use: SURVIVED,
// TIMEOUT, and UNKNOWN are actionable; KILLED, INVALID, and
// UNSUPPORTED are not. See docs/semantics.md.
//
// This alone is too coarse to classify a transition between two
// actionable verdicts, or between two non-actionable ones -- see
// Compare's own doc comment for the finer distinctions
// (isConfirmedSurvivor, and current == KILLED specifically) it applies
// on top of this.
func Actionable(v model.Verdict) bool {
	switch v {
	case model.VerdictSurvived, model.VerdictTimeout, model.VerdictUnknown:
		return true
	default:
		return false
	}
}

// isConfirmedSurvivor reports whether v is the one verdict that
// represents a demonstrated, concrete test gap: SURVIVED. TIMEOUT and
// UNKNOWN are also Actionable -- the run didn't positively confirm the
// mutant was caught -- but neither is a confirmed gap the way SURVIVED
// is: both mean the run was inconclusive (a deadline expired, or the
// backend couldn't classify what happened), not that the test suite
// was shown not to catch the mutant.
func isConfirmedSurvivor(v model.Verdict) bool {
	return v == model.VerdictSurvived
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
	// Baseline: either a previously-killed (or otherwise unactionable)
	// mutant regressed, a brand-new mutant (from new code) was
	// actionable from the start, or a mutant went from an inconclusive
	// actionable verdict (TIMEOUT, UNKNOWN) to a confirmed one
	// (SURVIVED) -- see isConfirmedSurvivor. That last case is not a
	// "newly actionable" transition under Actionable alone (both sides
	// are actionable), but it is genuinely new information: what was
	// previously an inconclusive run is now a demonstrated gap. These
	// are the entries worth a reviewer's attention.
	NewSurvivors []MutantDiff `json:"new_survivors"`
	// FixedSurvivors are mutant IDs actionable in Baseline that are
	// still present in Current with a Current verdict of exactly
	// KILLED -- killed by an improved test, not merely removed and not
	// merely reclassified. This is a genuine test-quality signal; a
	// removed mutant is not (see RemovedMutants below), and neither is
	// a mutant that became unactionable for a reason other than a real
	// kill (see Reclassified below) -- both used to be folded in here,
	// which overstated what actually happened.
	FixedSurvivors []MutantDiff `json:"fixed_survivors"`
	// StillOpen are mutant IDs actionable on both sides with a
	// different verdict, where neither side is the confirmed-gap case
	// that promotes an entry to NewSurvivors above: SURVIVED regressing
	// to an inconclusive TIMEOUT or UNKNOWN, or a swap between the two
	// inconclusive verdicts themselves. None of these are a fix --
	// nothing demonstrated the mutant is now caught -- but folding them
	// silently into UnchangedCount would hide that the verdict did
	// change; a reviewer relying on an unchanged count staying flat
	// should still see this.
	StillOpen []MutantDiff `json:"still_open"`
	// Reclassified are mutant IDs actionable in Baseline that became
	// unactionable in Current for a reason other than a real kill --
	// Current's verdict is INVALID, EQUIVALENT, or UNSUPPORTED, not
	// KILLED. None of those mean a test now catches this mutant: INVALID
	// means it no longer even compiles in this configuration, EQUIVALENT
	// means discovery proved it behaviorally identical to the original
	// (a proof, not a kill), and UNSUPPORTED means the backend declined
	// it outright. Counting any of these as "fixed" credits a test
	// improvement that never happened.
	Reclassified []MutantDiff `json:"reclassified"`
	// RemovedMutants are mutant IDs present in Baseline that are
	// absent from Current entirely -- that mutation site no longer
	// exists, usually because the code there was deleted, rewritten,
	// or moved (which, given ID-matching's own limitation, also
	// covers a site whose ID merely shifted due to an earlier edit in
	// the same file; see the package doc comment). This holds
	// regardless of the mutant's prior verdict: a removed survivor
	// and a removed kill are both here, distinguished by each entry's
	// own Baseline.Verdict, since neither is a "fix" by a better
	// test -- the code just isn't there to test anymore.
	RemovedMutants []MutantDiff `json:"removed_mutants"`
	// UnchangedCount is every other mutant ID appearing in either
	// report: present in both sides with the exact same verdict,
	// present in both sides as non-actionable on both sides with no
	// promotion to Reclassified applying (Current's verdict wasn't a
	// real kill, so there was nothing to reclassify away from), or
	// present only in Current and not actionable (new code with
	// nothing to flag) -- so there's nothing to report about it.
	// NewSurvivors, FixedSurvivors, StillOpen, Reclassified,
	// RemovedMutants, and UnchangedCount always sum to the total
	// number of distinct mutant IDs across both reports.
	UnchangedCount int     `json:"unchanged_count"`
	BaselineScore  float64 `json:"baseline_score"`
	CurrentScore   float64 `json:"current_score"`
	BaselineText   string  `json:"baseline_score_text"`
	CurrentText    string  `json:"current_score_text"`
	// LikelyShifted is an additional, purely informational
	// correlation layer over NewSurvivors and RemovedMutants: pairs
	// this package's fingerprint believes are the same logical
	// mutation, just relocated by an unrelated earlier edit shifting
	// its byte-offset-based ID (see docs/limitations.md limitation
	// 12). Nothing is removed from NewSurvivors, RemovedMutants, or
	// UnchangedCount on the strength of this -- see
	// findLikelyShifts's doc comment for why matching is
	// heuristic, not exact, and what specifically has to line up
	// before two entries are even considered.
	LikelyShifted []ShiftCandidate `json:"likely_shifted"`
}

// Compare builds a Diff between a baseline report (e.g. the base
// branch) and a current report (e.g. a pull request), matched by
// mutant ID. Every distinct mutant ID across both reports lands in
// exactly one of Diff's six buckets, classified in this order:
//
//  1. Absent from Current entirely -> RemovedMutants, regardless of
//     its Baseline verdict.
//  2. Not actionable in Baseline (or absent from it), actionable in
//     Current -> NewSurvivors.
//  3. Actionable in both, verdict differs, and Current is the
//     confirmed-gap verdict (SURVIVED) while Baseline was not ->
//     NewSurvivors: genuinely new information even though Actionable
//     alone says nothing changed.
//  4. Actionable in both, verdict differs, and case 3 doesn't apply
//     (SURVIVED regressing to an inconclusive verdict, or a swap
//     between the two inconclusive verdicts) -> StillOpen.
//  5. Actionable in Baseline, not actionable in Current, and Current
//     is exactly KILLED -> FixedSurvivors.
//  6. Actionable in Baseline, not actionable in Current, and Current
//     is not KILLED (INVALID, EQUIVALENT, or UNSUPPORTED) ->
//     Reclassified: not a fix, since nothing killed it.
//  7. Everything else -> UnchangedCount.
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

		if !hasC {
			// Removed takes priority over every other classification:
			// there is no "current" state to call fixed or unchanged,
			// only a baseline one that no longer has anywhere to
			// apply.
			d.RemovedMutants = append(d.RemovedMutants, MutantDiff{ID: id, Baseline: resultPtr(b, hasB)})
			return
		}

		bActionable := hasB && Actionable(b.Verdict)
		cActionable := Actionable(c.Verdict)
		md := MutantDiff{ID: id, Current: resultPtr(c, hasC)}
		if hasB {
			md.Baseline = resultPtr(b, hasB)
		}

		switch {
		case !bActionable && cActionable:
			d.NewSurvivors = append(d.NewSurvivors, md)
		case bActionable && cActionable && b.Verdict != c.Verdict:
			if isConfirmedSurvivor(c.Verdict) && !isConfirmedSurvivor(b.Verdict) {
				d.NewSurvivors = append(d.NewSurvivors, md)
			} else {
				d.StillOpen = append(d.StillOpen, md)
			}
		case bActionable && !cActionable:
			if c.Verdict == model.VerdictKilled {
				d.FixedSurvivors = append(d.FixedSurvivors, md)
			} else {
				d.Reclassified = append(d.Reclassified, md)
			}
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
	sortByLocation(d.StillOpen)
	sortByLocation(d.Reclassified)
	sortByLocation(d.RemovedMutants)
	d.findLikelyShifts()
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
