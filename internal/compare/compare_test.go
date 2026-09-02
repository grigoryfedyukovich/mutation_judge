package compare

import (
	"bytes"
	"strings"
	"testing"

	"github.com/example/mutation-judge/internal/model"
)

func mutantResult(id string, verdict model.Verdict, file string, line int) model.Result {
	return model.Result{
		Verdict: verdict,
		Mutation: model.Mutation{
			ID: id, RuleID: "MJ-BOUNDARY", Operator: "boundary",
			Span:        model.Span{File: file, StartLine: line, StartCol: 1},
			Description: "replace comparison > with >=",
			Suggestion:  "exercise the zero case",
		},
	}
}

func TestCompareClassifiesAllSixBuckets(t *testing.T) {
	baseline := model.Report{
		Summary: model.Summary{Score: 50, ScoreText: "50.0%"},
		Results: []model.Result{
			mutantResult("M-still-killed", model.VerdictKilled, "a.go", 1),
			mutantResult("M-still-survived", model.VerdictSurvived, "b.go", 2),
			mutantResult("M-regressed", model.VerdictKilled, "c.go", 3),          // killed -> survived: NEW survivor
			mutantResult("M-fixed", model.VerdictSurvived, "d.go", 4),            // survived -> killed, still present: FIXED
			mutantResult("M-removed-survivor", model.VerdictSurvived, "e.go", 5), // gone entirely: REMOVED
			mutantResult("M-removed-killed", model.VerdictKilled, "g.go", 7),     // also gone entirely: REMOVED, not unchanged
			mutantResult("M-confirmed", model.VerdictTimeout, "h.go", 8),         // timeout -> survived: NEW survivor (confirmed gap)
			mutantResult("M-regressed-inconclusive", model.VerdictSurvived, "i.go", 9), // survived -> timeout: STILL OPEN, not unchanged
			mutantResult("M-reclassified", model.VerdictSurvived, "j.go", 10),    // survived -> invalid: RECLASSIFIED, not fixed
		},
	}
	current := model.Report{
		Summary: model.Summary{Score: 60, ScoreText: "60.0%"},
		Results: []model.Result{
			mutantResult("M-still-killed", model.VerdictKilled, "a.go", 1),
			mutantResult("M-still-survived", model.VerdictSurvived, "b.go", 2),
			mutantResult("M-regressed", model.VerdictSurvived, "c.go", 3),
			mutantResult("M-fixed", model.VerdictKilled, "d.go", 4),
			mutantResult("M-brand-new", model.VerdictSurvived, "f.go", 6), // new mutant, actionable: NEW survivor
			mutantResult("M-confirmed", model.VerdictSurvived, "h.go", 8),
			mutantResult("M-regressed-inconclusive", model.VerdictTimeout, "i.go", 9),
			mutantResult("M-reclassified", model.VerdictInvalid, "j.go", 10),
		},
	}

	d := Compare(baseline, current)

	if len(d.NewSurvivors) != 3 {
		t.Fatalf("expected 3 new survivors, got %d: %+v", len(d.NewSurvivors), d.NewSurvivors)
	}
	newIDs := map[string]bool{}
	for _, md := range d.NewSurvivors {
		newIDs[md.ID] = true
	}
	if !newIDs["M-regressed"] || !newIDs["M-brand-new"] || !newIDs["M-confirmed"] {
		t.Fatalf("wrong new survivors: %+v", d.NewSurvivors)
	}

	if len(d.FixedSurvivors) != 1 {
		t.Fatalf("expected exactly 1 fixed survivor (still present, no longer actionable), got %d: %+v", len(d.FixedSurvivors), d.FixedSurvivors)
	}
	if d.FixedSurvivors[0].ID != "M-fixed" {
		t.Fatalf("wrong fixed survivor: %+v", d.FixedSurvivors[0])
	}
	if d.FixedSurvivors[0].Current == nil {
		t.Fatal("a fixed survivor must still have a Current result -- that's what distinguishes it from removed")
	}

	if len(d.StillOpen) != 1 || d.StillOpen[0].ID != "M-regressed-inconclusive" {
		t.Fatalf("expected exactly M-regressed-inconclusive in StillOpen, got: %+v", d.StillOpen)
	}

	if len(d.Reclassified) != 1 || d.Reclassified[0].ID != "M-reclassified" {
		t.Fatalf("expected exactly M-reclassified in Reclassified, got: %+v", d.Reclassified)
	}

	if len(d.RemovedMutants) != 2 {
		t.Fatalf("expected 2 removed mutants (one formerly-survived, one formerly-killed), got %d: %+v", len(d.RemovedMutants), d.RemovedMutants)
	}
	removedIDs := map[string]bool{}
	for _, md := range d.RemovedMutants {
		removedIDs[md.ID] = true
		if md.Current != nil {
			t.Fatalf("a removed mutant must have a nil Current: %+v", md)
		}
	}
	if !removedIDs["M-removed-survivor"] || !removedIDs["M-removed-killed"] {
		t.Fatalf("wrong removed mutants: %+v", d.RemovedMutants)
	}

	// still-killed and still-survived: 2 unchanged.
	if d.UnchangedCount != 2 {
		t.Fatalf("expected 2 unchanged, got %d", d.UnchangedCount)
	}

	// Every distinct ID across both reports must land in exactly one bucket.
	total := len(d.NewSurvivors) + len(d.FixedSurvivors) + len(d.StillOpen) + len(d.Reclassified) + len(d.RemovedMutants) + d.UnchangedCount
	if total != 10 { // 9 baseline + 1 brand-new not in baseline = 10 distinct IDs
		t.Fatalf("buckets don't partition the full ID set: total=%d", total)
	}
}

// TestCompareTimeoutToSurvivedIsConfirmedGapNotUnchanged and
// TestCompareSurvivedToTimeoutIsStillOpenNotUnchanged are the direct
// regression tests for the real bug: both TIMEOUT and SURVIVED are
// Actionable, so before this fix both transitions fell through to the
// default case and were silently counted as UnchangedCount, hiding a
// confirmed new gap in one direction and a confidence regression in
// the other.
func TestCompareTimeoutToSurvivedIsConfirmedGapNotUnchanged(t *testing.T) {
	baseline := model.Report{Results: []model.Result{mutantResult("M-x", model.VerdictTimeout, "a.go", 1)}}
	current := model.Report{Results: []model.Result{mutantResult("M-x", model.VerdictSurvived, "a.go", 1)}}
	d := Compare(baseline, current)
	if len(d.NewSurvivors) != 1 || d.NewSurvivors[0].ID != "M-x" {
		t.Fatalf("expected M-x in NewSurvivors (a confirmed gap, not merely still actionable), got NewSurvivors=%+v UnchangedCount=%d", d.NewSurvivors, d.UnchangedCount)
	}
	if d.UnchangedCount != 0 {
		t.Fatalf("TIMEOUT -> SURVIVED must not also be counted unchanged, got UnchangedCount=%d", d.UnchangedCount)
	}
}

func TestCompareSurvivedToTimeoutIsStillOpenNotUnchanged(t *testing.T) {
	baseline := model.Report{Results: []model.Result{mutantResult("M-x", model.VerdictSurvived, "a.go", 1)}}
	current := model.Report{Results: []model.Result{mutantResult("M-x", model.VerdictTimeout, "a.go", 1)}}
	d := Compare(baseline, current)
	if len(d.StillOpen) != 1 || d.StillOpen[0].ID != "M-x" {
		t.Fatalf("expected M-x in StillOpen, got StillOpen=%+v UnchangedCount=%d", d.StillOpen, d.UnchangedCount)
	}
	if len(d.FixedSurvivors) != 0 {
		t.Fatalf("SURVIVED -> TIMEOUT must not be counted fixed -- nothing demonstrated the mutant is caught, got FixedSurvivors=%+v", d.FixedSurvivors)
	}
	if d.UnchangedCount != 0 {
		t.Fatalf("SURVIVED -> TIMEOUT must not also be counted unchanged, got UnchangedCount=%d", d.UnchangedCount)
	}
}

// TestCompareNeverCallsInvalidOrEquivalentOrUnsupportedAFix is the
// direct regression test for the other real bug: SURVIVED -> INVALID
// and SURVIVED -> EQUIVALENT both used to land in FixedSurvivors
// merely because INVALID/EQUIVALENT aren't Actionable, crediting a
// test improvement that never happened -- the mutant was reclassified
// (no longer compiles, or proven equivalent), not killed by a test.
// UNSUPPORTED is included for the same reason, even though no backend
// in this codebase currently emits it (docs/semantics.md: "reserved
// for an input or operator a backend explicitly declines").
func TestCompareNeverCallsInvalidOrEquivalentOrUnsupportedAFix(t *testing.T) {
	for _, v := range []model.Verdict{model.VerdictInvalid, model.VerdictEquivalent, model.VerdictUnsupported} {
		t.Run(string(v), func(t *testing.T) {
			baseline := model.Report{Results: []model.Result{mutantResult("M-x", model.VerdictSurvived, "a.go", 1)}}
			current := model.Report{Results: []model.Result{mutantResult("M-x", v, "a.go", 1)}}
			d := Compare(baseline, current)
			if len(d.FixedSurvivors) != 0 {
				t.Fatalf("SURVIVED -> %s must not be counted fixed, got FixedSurvivors=%+v", v, d.FixedSurvivors)
			}
			if len(d.Reclassified) != 1 || d.Reclassified[0].ID != "M-x" {
				t.Fatalf("expected M-x in Reclassified, got: %+v", d.Reclassified)
			}
		})
	}
}

func TestCompareRemovedMutantHasNilCurrentAndReadableMutation(t *testing.T) {
	baseline := model.Report{Results: []model.Result{mutantResult("M-gone", model.VerdictSurvived, "a.go", 1)}}
	current := model.Report{}
	d := Compare(baseline, current)
	if len(d.RemovedMutants) != 1 {
		t.Fatalf("expected 1 removed mutant, got %d", len(d.RemovedMutants))
	}
	if d.RemovedMutants[0].Current != nil {
		t.Fatal("removed mutant must have a nil Current side")
	}
	if d.RemovedMutants[0].Baseline == nil {
		t.Fatal("removed mutant must retain its Baseline side")
	}
	if d.RemovedMutants[0].Mutation().Span.File != "a.go" {
		t.Fatal("Mutation() must still be readable from the Baseline side alone")
	}
}

// TestCompareRemovedKilledMutantIsNotCountedUnchanged is the specific
// case that makes RemovedMutants a distinct bucket at all rather than
// splitting off only removed survivors: a mutant that was KILLED (no
// story) and then its code disappeared is still real churn worth
// surfacing, not silently folded into "nothing changed here".
func TestCompareRemovedKilledMutantIsNotCountedUnchanged(t *testing.T) {
	baseline := model.Report{Results: []model.Result{mutantResult("M-gone", model.VerdictKilled, "a.go", 1)}}
	current := model.Report{}
	d := Compare(baseline, current)
	if len(d.RemovedMutants) != 1 || d.RemovedMutants[0].ID != "M-gone" {
		t.Fatalf("expected the formerly-killed mutant in RemovedMutants, got: %+v", d.RemovedMutants)
	}
	if d.UnchangedCount != 0 {
		t.Fatalf("a removed mutant must not also be counted unchanged, got UnchangedCount=%d", d.UnchangedCount)
	}
}

func TestRenderTextShowsSuggestionForNewSurvivorsOnly(t *testing.T) {
	baseline := model.Report{Summary: model.Summary{ScoreText: "50.0%"}}
	current := model.Report{
		Summary: model.Summary{ScoreText: "40.0%"},
		Results: []model.Result{mutantResult("M-new", model.VerdictSurvived, "a.go", 10)},
	}
	d := Compare(baseline, current)
	var b bytes.Buffer
	if err := RenderText(&b, d); err != nil {
		t.Fatal(err)
	}
	out := b.String()
	if !strings.Contains(out, "new survivors: 1") || !strings.Contains(out, "a.go:10:1") {
		t.Fatalf("missing new survivor detail:\n%s", out)
	}
	if !strings.Contains(out, "suggested test: exercise the zero case") {
		t.Fatalf("missing suggestion text:\n%s", out)
	}
	if !strings.Contains(out, "fixed survivors: 0") || !strings.Contains(out, "removed mutants: 0") || !strings.Contains(out, "unchanged: 0") {
		t.Fatalf("missing zero-count sections:\n%s", out)
	}
}

func TestRenderTextDistinguishesFixedFromRemoved(t *testing.T) {
	baseline := model.Report{
		Summary: model.Summary{ScoreText: "0.0%"},
		Results: []model.Result{
			mutantResult("M-fixed", model.VerdictSurvived, "a.go", 1),
			mutantResult("M-removed", model.VerdictSurvived, "b.go", 2),
		},
	}
	current := model.Report{
		Summary: model.Summary{ScoreText: "100.0%"},
		Results: []model.Result{mutantResult("M-fixed", model.VerdictKilled, "a.go", 1)},
	}
	d := Compare(baseline, current)
	var b bytes.Buffer
	if err := RenderText(&b, d); err != nil {
		t.Fatal(err)
	}
	out := b.String()
	if !strings.Contains(out, "fixed survivors: 1") || !strings.Contains(out, "KILLED a.go:1:1") {
		t.Fatalf("missing fixed-survivor detail:\n%s", out)
	}
	if !strings.Contains(out, "removed mutants: 1") || !strings.Contains(out, "was SURVIVED b.go:2:1") {
		t.Fatalf("missing removed-mutant detail:\n%s", out)
	}
}

func TestRenderTextShowsStillOpenAndReclassifiedSections(t *testing.T) {
	baseline := model.Report{
		Summary: model.Summary{ScoreText: "50.0%"},
		Results: []model.Result{
			mutantResult("M-open", model.VerdictSurvived, "a.go", 1),
			mutantResult("M-reclassified", model.VerdictSurvived, "b.go", 2),
		},
	}
	current := model.Report{
		Summary: model.Summary{ScoreText: "50.0%"},
		Results: []model.Result{
			mutantResult("M-open", model.VerdictTimeout, "a.go", 1),
			mutantResult("M-reclassified", model.VerdictInvalid, "b.go", 2),
		},
	}
	d := Compare(baseline, current)
	var b bytes.Buffer
	if err := RenderText(&b, d); err != nil {
		t.Fatal(err)
	}
	out := b.String()
	if !strings.Contains(out, "still open: 1") || !strings.Contains(out, "TIMEOUT a.go:1:1") {
		t.Fatalf("missing still-open detail:\n%s", out)
	}
	if !strings.Contains(out, "reclassified: 1") || !strings.Contains(out, "INVALID b.go:2:1") {
		t.Fatalf("missing reclassified detail:\n%s", out)
	}
	if !strings.Contains(out, "not a test fix") {
		t.Fatalf("reclassified entries must not read as a fix:\n%s", out)
	}
}

func TestRenderJSONRoundTrips(t *testing.T) {
	baseline := model.Report{Results: []model.Result{mutantResult("M-x", model.VerdictKilled, "a.go", 1)}}
	current := model.Report{Results: []model.Result{mutantResult("M-x", model.VerdictSurvived, "a.go", 1)}}
	d := Compare(baseline, current)
	var b bytes.Buffer
	if err := RenderJSON(&b, d); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(b.String(), `"id": "M-x"`) {
		t.Fatalf("expected mutant ID in JSON output:\n%s", b.String())
	}
}

func TestRenderJSONEmptyBucketsAreArraysNotNull(t *testing.T) {
	d := Compare(model.Report{}, model.Report{})
	var b bytes.Buffer
	if err := RenderJSON(&b, d); err != nil {
		t.Fatal(err)
	}
	out := b.String()
	if strings.Contains(out, "null") {
		t.Fatalf("every bucket (new_survivors/fixed_survivors/still_open/reclassified/removed_mutants) must serialize as [], not null:\n%s", out)
	}
}

func TestRenderJSONIncludesAllBucketKeys(t *testing.T) {
	d := Compare(model.Report{}, model.Report{})
	var b bytes.Buffer
	if err := RenderJSON(&b, d); err != nil {
		t.Fatal(err)
	}
	out := b.String()
	for _, key := range []string{`"new_survivors"`, `"fixed_survivors"`, `"still_open"`, `"reclassified"`, `"removed_mutants"`, `"unchanged_count"`} {
		if !strings.Contains(out, key) {
			t.Fatalf("expected JSON key %s for CI to consume, got:\n%s", key, out)
		}
	}
}
