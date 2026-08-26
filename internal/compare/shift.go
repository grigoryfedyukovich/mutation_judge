package compare

import (
	"sort"
	"strconv"
	"strings"

	"github.com/example/mutation-judge/internal/model"
)

// ShiftCandidate is one unambiguous correlation between a removed
// mutant and a new-mutant-ID that this package's fingerprint believes
// is actually the same logical mutation, just relocated by an
// unrelated earlier edit in the same file shifting its byte offset
// (and so its ID -- see internal/frontend.mutationID and this
// package's own doc comment). It is informational only: Compare never
// removes the underlying entries from NewSurvivors or RemovedMutants
// on the strength of a ShiftCandidate, since the fingerprint is a
// heuristic, not the exact-ID identity the rest of this package
// relies on. See findLikelyShifts's doc comment for exactly what has
// to match and why a match can still be wrong.
type ShiftCandidate struct {
	RemovedID string `json:"removed_id"`
	NewID     string `json:"new_id"`
	File      string `json:"file"`
	OldLine   int    `json:"old_line"`
	NewLine   int    `json:"new_line"`
	// VerdictChanged is true when the mutant's actionable-status
	// genuinely differs between the two sides despite the apparent
	// shift -- e.g. it was KILLED before the edit and is SURVIVED
	// after. That's still a real, correctly-counted new survivor;
	// the shift correlation is extra context; it is never grounds to
	// discount a genuine regression as "just noise".
	VerdictChanged bool   `json:"verdict_changed"`
	Note           string `json:"note"`
}

// findLikelyShifts scans RemovedMutants and the subset of NewSurvivors
// with no baseline ID at all (a brand-new ID, not a same-ID
// regression -- those already have an exact explanation and need no
// heuristic) for pairs that share a fingerprint: same file, same
// operator, rule, original, and replacement text, same column span
// (byte-offset shifts move a mutation vertically, not horizontally,
// unless the edit is on the mutated line itself, which this
// deliberately does not try to handle), and identical pre-mutation
// source line text (parsed from each mutation's own unified diff) --
// this last check is what tells two structurally identical
// comparisons elsewhere in the same file (same operator, same column)
// apart, since their surrounding variable names will differ.
//
// A fingerprint is only ever accepted as a match when it identifies
// exactly one candidate on each side. Zero or multiple candidates on
// either side means the fingerprint doesn't disambiguate -- correctly
// leaving those entries unreconciled rather than guessing.
func (d *Diff) findLikelyShifts() {
	removedByFP := map[string][]int{}
	for i, md := range d.RemovedMutants {
		if md.Baseline == nil {
			continue
		}
		removedByFP[shiftFingerprint(md.Baseline.Mutation)] = append(removedByFP[shiftFingerprint(md.Baseline.Mutation)], i)
	}
	newByFP := map[string][]int{}
	for i, md := range d.NewSurvivors {
		if md.Current == nil || md.Baseline != nil {
			continue
		}
		newByFP[shiftFingerprint(md.Current.Mutation)] = append(newByFP[shiftFingerprint(md.Current.Mutation)], i)
	}

	var fps []string
	for fp := range removedByFP {
		fps = append(fps, fp)
	}
	sort.Strings(fps)

	for _, fp := range fps {
		removedIdxs := removedByFP[fp]
		newIdxs, ok := newByFP[fp]
		if !ok || len(removedIdxs) != 1 || len(newIdxs) != 1 {
			continue
		}
		r := d.RemovedMutants[removedIdxs[0]]
		n := d.NewSurvivors[newIdxs[0]]
		wasActionable := Actionable(r.Baseline.Verdict)
		verdictChanged := !wasActionable // n.Current is always actionable, by construction of NewSurvivors
		note := "likely the same mutation, relocated by an earlier unrelated edit in this file -- not a real change"
		if verdictChanged {
			note = "likely the same mutation, relocated by an earlier unrelated edit in this file, but its verdict also genuinely changed (was " + string(r.Baseline.Verdict) + ") -- a real regression, not just noise from the shift"
		}
		d.LikelyShifted = append(d.LikelyShifted, ShiftCandidate{
			RemovedID: r.ID, NewID: n.ID, File: r.Mutation().Span.File,
			OldLine: r.Mutation().Span.StartLine, NewLine: n.Mutation().Span.StartLine,
			VerdictChanged: verdictChanged, Note: note,
		})
	}
}

// shiftFingerprint is deliberately narrow: every field it includes
// exists specifically to rule out a false match (see the two
// negative tests in shift_test.go for what each one catches), and it
// intentionally excludes the file's byte offset, line number, and ID
// -- those are exactly what an unrelated edit changes, so matching on
// any of them would defeat the point.
func shiftFingerprint(m model.Mutation) string {
	return strings.Join([]string{
		m.Span.File, m.Operator, m.RuleID, m.Original, m.Replacement,
		strconv.Itoa(m.Span.StartCol), strconv.Itoa(m.Span.EndCol),
		diffOldText(m.Diff),
	}, "\x00")
}

// diffOldText extracts the pre-mutation source line(s) from a
// Mutation's own unified diff (see internal/frontend.unifiedDiff for
// the exact format this parses): every "-"-prefixed content line
// between the "@@ ... @@" hunk header and the first "+"-prefixed
// line. This is what lets the fingerprint tell two structurally
// identical comparisons at the same column in the same file (e.g. two
// similar-looking if statements) apart -- their surrounding variable
// names will differ even when everything else about the mutation
// matches.
func diffOldText(diff string) string {
	var old []string
	inHunk := false
	for _, line := range strings.Split(diff, "\n") {
		switch {
		case strings.HasPrefix(line, "@@ "):
			inHunk = true
		case !inHunk:
			// still in the "--- a/..." / "+++ b/..." header lines
		case strings.HasPrefix(line, "+"):
			return strings.Join(old, "\n")
		case strings.HasPrefix(line, "-"):
			old = append(old, strings.TrimPrefix(line, "-"))
		}
	}
	return strings.Join(old, "\n")
}
