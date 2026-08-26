package compare

import (
	"bytes"
	"fmt"
	"strings"
	"testing"

	"github.com/example/mutation-judge/internal/model"
)

// fakeDiff builds a unified diff in exactly the shape
// internal/frontend.unifiedDiff produces, so diffOldText's parsing is
// exercised against the real format, not a simplified stand-in.
func fakeDiff(file string, line int, oldText, newText string) string {
	return fmt.Sprintf("--- a/%s\n+++ b/%s\n@@ -%d,1 +%d,1 @@\n-%s\n+%s\n", file, file, line, line, oldText, newText)
}

func boundaryResult(id string, verdict model.Verdict, file string, line, col int, lineText string) model.Result {
	newText := strings.Replace(lineText, "> 0", ">= 0", 1)
	return model.Result{
		Verdict: verdict,
		Mutation: model.Mutation{
			ID: id, Operator: "boundary", RuleID: "MJ-BOUNDARY",
			Span:     model.Span{File: file, StartLine: line, StartCol: col, EndLine: line, EndCol: col + 1},
			Original: ">", Replacement: ">=",
			Description: "replace comparison > with >=",
			Diff:        fakeDiff(file, line, lineText, newText),
		},
	}
}

func TestFindLikelyShiftsMatchesUnambiguousPair(t *testing.T) {
	baseline := model.Report{Results: []model.Result{boundaryResult("M-old", model.VerdictSurvived, "a.go", 5, 7, "\tif n > 0 {")}}
	current := model.Report{Results: []model.Result{boundaryResult("M-new", model.VerdictSurvived, "a.go", 12, 7, "\tif n > 0 {")}}

	d := Compare(baseline, current)

	if len(d.RemovedMutants) != 1 || len(d.NewSurvivors) != 1 {
		t.Fatalf("expected exactly 1 removed and 1 new (the strict classification must be unaffected), got removed=%d new=%d", len(d.RemovedMutants), len(d.NewSurvivors))
	}
	if len(d.LikelyShifted) != 1 {
		t.Fatalf("expected exactly 1 likely-shifted correlation, got %d: %+v", len(d.LikelyShifted), d.LikelyShifted)
	}
	sc := d.LikelyShifted[0]
	if sc.RemovedID != "M-old" || sc.NewID != "M-new" || sc.OldLine != 5 || sc.NewLine != 12 {
		t.Fatalf("unexpected shift candidate: %+v", sc)
	}
	if sc.VerdictChanged {
		t.Fatalf("both sides were SURVIVED; this must not be reported as a verdict change: %+v", sc)
	}
}

func TestFindLikelyShiftsFlagsGenuineRegressionSeparately(t *testing.T) {
	// Same fingerprint (file/operator/rule/original/replacement/col/
	// line text), but the baseline side was KILLED, not SURVIVED --
	// this is a real regression coinciding with a shift, not mere
	// noise, and must say so.
	baseline := model.Report{Results: []model.Result{boundaryResult("M-old", model.VerdictKilled, "a.go", 5, 7, "\tif n > 0 {")}}
	current := model.Report{Results: []model.Result{boundaryResult("M-new", model.VerdictSurvived, "a.go", 12, 7, "\tif n > 0 {")}}

	d := Compare(baseline, current)

	if len(d.NewSurvivors) != 1 {
		t.Fatalf("a genuine regression must still be counted as a new survivor, got %d", len(d.NewSurvivors))
	}
	if len(d.LikelyShifted) != 1 || !d.LikelyShifted[0].VerdictChanged {
		t.Fatalf("expected exactly 1 shift candidate flagged as a verdict change, got: %+v", d.LikelyShifted)
	}
}

func TestFindLikelyShiftsRefusesAmbiguousMatches(t *testing.T) {
	// Two removed candidates share a fingerprint with the one new
	// candidate -- correctly conservative behavior is to report
	// neither as a shift, not guess which one it really is.
	baseline := model.Report{Results: []model.Result{
		boundaryResult("M-old-1", model.VerdictSurvived, "a.go", 5, 7, "\tif n > 0 {"),
		boundaryResult("M-old-2", model.VerdictSurvived, "a.go", 20, 7, "\tif n > 0 {"),
	}}
	current := model.Report{Results: []model.Result{
		boundaryResult("M-new", model.VerdictSurvived, "a.go", 12, 7, "\tif n > 0 {"),
	}}

	d := Compare(baseline, current)

	if len(d.RemovedMutants) != 2 || len(d.NewSurvivors) != 1 {
		t.Fatalf("strict classification must be untouched: removed=%d new=%d", len(d.RemovedMutants), len(d.NewSurvivors))
	}
	if len(d.LikelyShifted) != 0 {
		t.Fatalf("an ambiguous 2-vs-1 match must not be reported as a shift, got: %+v", d.LikelyShifted)
	}
}

func TestFindLikelyShiftsRequiresIdenticalLineTextNotJustColumn(t *testing.T) {
	// Same file, operator, rule, original, replacement, and column --
	// but a different variable name on the line itself. Column alone
	// is not enough; these are two different comparisons that merely
	// happen to sit at the same indentation depth.
	baseline := model.Report{Results: []model.Result{boundaryResult("M-old", model.VerdictSurvived, "a.go", 5, 7, "\tif n > 0 {")}}
	current := model.Report{Results: []model.Result{boundaryResult("M-new", model.VerdictSurvived, "a.go", 12, 7, "\tif m > 0 {")}}

	d := Compare(baseline, current)

	if len(d.LikelyShifted) != 0 {
		t.Fatalf("different surrounding line text must not be treated as a shift of the same mutation: %+v", d.LikelyShifted)
	}
}

func TestFindLikelyShiftsDoesNotConsiderAlreadyIDMatchedRegressions(t *testing.T) {
	// M-same-id survives in both baseline and current under the exact
	// same ID (a real, exact-ID regression) -- it should never be
	// pulled into shift-candidate matching at all, since it already
	// has an exact explanation and isn't in RemovedMutants to begin
	// with.
	baseline := model.Report{Results: []model.Result{
		boundaryResult("M-same-id", model.VerdictKilled, "a.go", 5, 7, "\tif n > 0 {"),
	}}
	current := model.Report{Results: []model.Result{
		boundaryResult("M-same-id", model.VerdictSurvived, "a.go", 5, 7, "\tif n > 0 {"),
	}}

	d := Compare(baseline, current)

	if len(d.NewSurvivors) != 1 || d.NewSurvivors[0].Baseline == nil {
		t.Fatalf("expected the exact-ID regression to be in NewSurvivors with a Baseline side, got: %+v", d.NewSurvivors)
	}
	if len(d.LikelyShifted) != 0 {
		t.Fatalf("an exact-ID match needs no shift heuristic: %+v", d.LikelyShifted)
	}
}

func TestDiffOldTextParsesRealUnifiedDiffFormat(t *testing.T) {
	diff := fakeDiff("a.go", 5, "\tif n > 0 {", "\tif n >= 0 {")
	if got := diffOldText(diff); got != "\tif n > 0 {" {
		t.Fatalf("diffOldText = %q, want %q", got, "\tif n > 0 {")
	}
}

func TestRenderTextIncludesShiftNote(t *testing.T) {
	baseline := model.Report{
		Summary: model.Summary{ScoreText: "0.0%"},
		Results: []model.Result{boundaryResult("M-old", model.VerdictSurvived, "a.go", 5, 7, "\tif n > 0 {")},
	}
	current := model.Report{
		Summary: model.Summary{ScoreText: "0.0%"},
		Results: []model.Result{boundaryResult("M-new", model.VerdictSurvived, "a.go", 12, 7, "\tif n > 0 {")},
	}
	d := Compare(baseline, current)
	var b bytes.Buffer
	if err := RenderText(&b, d); err != nil {
		t.Fatal(err)
	}
	out := b.String()
	if !strings.Contains(out, "likely shifted") {
		t.Fatalf("expected a likely-shifted section in text output:\n%s", out)
	}
	if !strings.Contains(out, "likely shifted from the removed entry at a.go:5") {
		t.Fatalf("expected the new-survivor entry to point at its removed counterpart's location:\n%s", out)
	}
	if !strings.Contains(out, "likely shifted to the new entry at a.go:12") {
		t.Fatalf("expected the removed entry to point at its new counterpart's location:\n%s", out)
	}
}
