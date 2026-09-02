package compare

import (
	"encoding/json"
	"fmt"
	"io"

	"github.com/example/mutation-judge/internal/model"
)

// RenderText writes a human-readable diff summary, with per-mutant
// detail for new survivors, fixed survivors, still-open mutants,
// reclassified mutants, and removed mutants (the five buckets with
// something to say) and a bare count for the unchanged majority. Any
// entry findLikelyShifts correlated with one on the other side gets an
// inline note pointing at its counterpart, so a reader can immediately
// tell "probably just moved" from "a real change" without
// cross-referencing IDs by hand.
func RenderText(w io.Writer, d Diff) error {
	byRemovedID, byNewID := map[string]ShiftCandidate{}, map[string]ShiftCandidate{}
	for _, sc := range d.LikelyShifted {
		byRemovedID[sc.RemovedID] = sc
		byNewID[sc.NewID] = sc
	}

	ew := &errWriter{w: w}
	ew.printf("compare: baseline vs current\n  baseline score: %s\n  current score:  %s\n\n", d.BaselineText, d.CurrentText)

	ew.printf("new survivors: %d\n", len(d.NewSurvivors))
	for _, md := range d.NewSurvivors {
		m := md.Mutation()
		ew.printf("  %s %s:%d:%d %s\n", string(currentVerdict(md, "SURVIVED")), m.Span.File, m.Span.StartLine, m.Span.StartCol, m.Description)
		if md.Baseline != nil {
			ew.printf("    (was %s in the baseline report)\n", md.Baseline.Verdict)
		}
		if md.Current != nil && md.Current.Mutation.Suggestion != "" {
			ew.printf("    suggested test: %s\n", md.Current.Mutation.Suggestion)
		}
		if md.Baseline == nil {
			ew.printf("    (new mutant: not present in the baseline report)\n")
		}
		if sc, ok := byNewID[md.ID]; ok {
			ew.printf("    likely shifted from the removed entry at %s:%d (%s)\n", sc.File, sc.OldLine, sc.Note)
		}
	}

	ew.printf("\nfixed survivors: %d\n", len(d.FixedSurvivors))
	for _, md := range d.FixedSurvivors {
		m := md.Mutation()
		ew.printf("  %s %s:%d:%d %s\n", string(md.Current.Verdict), m.Span.File, m.Span.StartLine, m.Span.StartCol, m.Description)
		ew.printf("    (was %s in the baseline report)\n", md.Baseline.Verdict)
	}

	ew.printf("\nstill open: %d\n", len(d.StillOpen))
	for _, md := range d.StillOpen {
		m := md.Mutation()
		ew.printf("  %s %s:%d:%d %s\n", string(md.Current.Verdict), m.Span.File, m.Span.StartLine, m.Span.StartCol, m.Description)
		ew.printf("    (was %s in the baseline report -- not fixed, still actionable)\n", md.Baseline.Verdict)
	}

	ew.printf("\nreclassified: %d\n", len(d.Reclassified))
	for _, md := range d.Reclassified {
		m := md.Mutation()
		ew.printf("  %s %s:%d:%d %s\n", string(md.Current.Verdict), m.Span.File, m.Span.StartLine, m.Span.StartCol, m.Description)
		ew.printf("    (was %s in the baseline report -- not a test fix)\n", md.Baseline.Verdict)
	}

	ew.printf("\nremoved mutants: %d\n", len(d.RemovedMutants))
	for _, md := range d.RemovedMutants {
		m := md.Mutation()
		ew.printf("  was %s %s:%d:%d %s\n", string(md.Baseline.Verdict), m.Span.File, m.Span.StartLine, m.Span.StartCol, m.Description)
		ew.printf("    (no longer present in the current report)\n")
		if sc, ok := byRemovedID[md.ID]; ok {
			ew.printf("    likely shifted to the new entry at %s:%d (%s)\n", sc.File, sc.NewLine, sc.Note)
		}
	}

	ew.printf("\nunchanged: %d\n", d.UnchangedCount)
	return ew.err
}

// currentVerdict returns the current-side verdict as a string, or a
// fallback (used only defensively; NewSurvivors always has Current
// set by construction).
func currentVerdict(md MutantDiff, fallback string) model.Verdict {
	if md.Current != nil {
		return md.Current.Verdict
	}
	return model.Verdict(fallback)
}

// RenderJSON writes the Diff as JSON for scripting (e.g. a CI step
// that fails a build only on len(new_survivors) > 0). Every list
// field is guaranteed to serialize as [] rather than null when empty,
// so a consumer never needs a null-check before iterating.
func RenderJSON(w io.Writer, d Diff) error {
	if d.NewSurvivors == nil {
		d.NewSurvivors = []MutantDiff{}
	}
	if d.FixedSurvivors == nil {
		d.FixedSurvivors = []MutantDiff{}
	}
	if d.StillOpen == nil {
		d.StillOpen = []MutantDiff{}
	}
	if d.Reclassified == nil {
		d.Reclassified = []MutantDiff{}
	}
	if d.RemovedMutants == nil {
		d.RemovedMutants = []MutantDiff{}
	}
	if d.LikelyShifted == nil {
		d.LikelyShifted = []ShiftCandidate{}
	}
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(d)
}

type errWriter struct {
	w   io.Writer
	err error
}

func (e *errWriter) printf(format string, args ...any) {
	if e.err != nil {
		return
	}
	_, e.err = fmt.Fprintf(e.w, format, args...)
}
