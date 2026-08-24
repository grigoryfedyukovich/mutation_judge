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

func TestCompareClassifiesAllFourCases(t *testing.T) {
	baseline := model.Report{
		Summary: model.Summary{Score: 50, ScoreText: "50.0%"},
		Results: []model.Result{
			mutantResult("M-still-killed", model.VerdictKilled, "a.go", 1),
			mutantResult("M-still-survived", model.VerdictSurvived, "b.go", 2),
			mutantResult("M-regressed", model.VerdictKilled, "c.go", 3),          // killed -> survived: NEW survivor
			mutantResult("M-fixed", model.VerdictSurvived, "d.go", 4),            // survived -> killed: FIXED
			mutantResult("M-removed-survivor", model.VerdictSurvived, "e.go", 5), // gone in current: FIXED (removed)
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
		},
	}

	d := Compare(baseline, current)

	if len(d.NewSurvivors) != 2 {
		t.Fatalf("expected 2 new survivors, got %d: %+v", len(d.NewSurvivors), d.NewSurvivors)
	}
	newIDs := map[string]bool{}
	for _, md := range d.NewSurvivors {
		newIDs[md.ID] = true
	}
	if !newIDs["M-regressed"] || !newIDs["M-brand-new"] {
		t.Fatalf("wrong new survivors: %+v", d.NewSurvivors)
	}

	if len(d.FixedSurvivors) != 2 {
		t.Fatalf("expected 2 fixed survivors, got %d: %+v", len(d.FixedSurvivors), d.FixedSurvivors)
	}
	fixedIDs := map[string]bool{}
	for _, md := range d.FixedSurvivors {
		fixedIDs[md.ID] = true
	}
	if !fixedIDs["M-fixed"] || !fixedIDs["M-removed-survivor"] {
		t.Fatalf("wrong fixed survivors: %+v", d.FixedSurvivors)
	}

	// still-killed and still-survived: 2 unchanged.
	if d.UnchangedCount != 2 {
		t.Fatalf("expected 2 unchanged, got %d", d.UnchangedCount)
	}

	// Every distinct ID across both reports must land in exactly one bucket.
	total := len(d.NewSurvivors) + len(d.FixedSurvivors) + d.UnchangedCount
	if total != 6 { // 5 baseline + 1 brand-new not in baseline = 6 distinct IDs
		t.Fatalf("buckets don't partition the full ID set: total=%d", total)
	}
}

func TestCompareRemovedSurvivorHasNilCurrent(t *testing.T) {
	baseline := model.Report{Results: []model.Result{mutantResult("M-gone", model.VerdictSurvived, "a.go", 1)}}
	current := model.Report{}
	d := Compare(baseline, current)
	if len(d.FixedSurvivors) != 1 {
		t.Fatalf("expected 1 fixed survivor, got %d", len(d.FixedSurvivors))
	}
	if d.FixedSurvivors[0].Current != nil {
		t.Fatal("removed mutant must have a nil Current side")
	}
	if d.FixedSurvivors[0].Mutation().Span.File != "a.go" {
		t.Fatal("Mutation() must still be readable from the Baseline side alone")
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
	if !strings.Contains(out, "fixed survivors: 0") || !strings.Contains(out, "unchanged: 0") {
		t.Fatalf("missing zero-count sections:\n%s", out)
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
		t.Fatalf("new_survivors/fixed_survivors must serialize as [], not null:\n%s", out)
	}
}
