package compare

import (
	"bytes"
	"strings"
	"testing"

	"github.com/example/mutation-judge/internal/model"
)

func TestRenderHTMLShowsSuggestionForNewSurvivorsOnly(t *testing.T) {
	baseline := model.Report{Summary: model.Summary{ScoreText: "50.0%"}}
	current := model.Report{
		Summary: model.Summary{ScoreText: "40.0%"},
		Results: []model.Result{mutantResult("M-new", model.VerdictSurvived, "a.go", 10)},
	}
	d := Compare(baseline, current)
	var b bytes.Buffer
	if err := RenderHTML(&b, d); err != nil {
		t.Fatal(err)
	}
	out := b.String()
	if !strings.Contains(out, "New survivors (1)") || !strings.Contains(out, "a.go:10:1") {
		t.Fatalf("missing new survivor detail:\n%s", out)
	}
	if !strings.Contains(out, "Suggested test:</strong> exercise the zero case") {
		t.Fatalf("missing suggestion text:\n%s", out)
	}
	if !strings.Contains(out, "Fixed survivors (0)") || !strings.Contains(out, "Removed mutants (0)") {
		t.Fatalf("missing zero-count sections:\n%s", out)
	}
}

func TestRenderHTMLDistinguishesFixedFromRemoved(t *testing.T) {
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
	if err := RenderHTML(&b, d); err != nil {
		t.Fatal(err)
	}
	out := b.String()
	if !strings.Contains(out, "Fixed survivors (1)") || !strings.Contains(out, "KILLED <code>a.go:1:1") {
		t.Fatalf("missing fixed-survivor detail:\n%s", out)
	}
	if !strings.Contains(out, "Removed mutants (1)") || !strings.Contains(out, "was SURVIVED <code>b.go:2:1") {
		t.Fatalf("missing removed-mutant detail:\n%s", out)
	}
}

func TestRenderHTMLShowsStillOpenAndReclassifiedSections(t *testing.T) {
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
	if err := RenderHTML(&b, d); err != nil {
		t.Fatal(err)
	}
	out := b.String()
	if !strings.Contains(out, "Still open (1)") || !strings.Contains(out, "TIMEOUT <code>a.go:1:1") {
		t.Fatalf("missing still-open detail:\n%s", out)
	}
	if !strings.Contains(out, "Reclassified (1)") || !strings.Contains(out, "INVALID <code>b.go:2:1") {
		t.Fatalf("missing reclassified detail:\n%s", out)
	}
	if !strings.Contains(out, "not a test fix") {
		t.Fatalf("reclassified entries must not read as a fix:\n%s", out)
	}
}

// TestRenderHTMLIncludesShiftNote mirrors TestRenderTextIncludesShiftNote
// (shift_test.go), for the HTML output instead of text.
func TestRenderHTMLIncludesShiftNote(t *testing.T) {
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
	if err := RenderHTML(&b, d); err != nil {
		t.Fatal(err)
	}
	out := b.String()
	if !strings.Contains(out, "likely shifted from the removed entry at a.go:5") {
		t.Fatalf("expected the new-survivor entry to point at its removed counterpart's location:\n%s", out)
	}
	if !strings.Contains(out, "likely shifted to the new entry at a.go:12") {
		t.Fatalf("expected the removed entry to point at its new counterpart's location:\n%s", out)
	}
}

// TestRenderHTMLOmitsShiftNoteForUncorrelatedEntries guards the exact
// failure mode htmlData's doc comment describes: text/template's
// `with` treats a struct as truthy no matter its field values, so
// `{{with index someMap key}}` on a map of plain ShiftCandidate values
// would print a blank shift note for every entry, correlated or not,
// since a missing key still yields a (zero-value, but still a
// struct) ShiftCandidate. This mutant has no counterpart on the other
// side at all, so it must render with no shift note whatsoever.
func TestRenderHTMLOmitsShiftNoteForUncorrelatedEntries(t *testing.T) {
	baseline := model.Report{Summary: model.Summary{ScoreText: "50.0%"}}
	current := model.Report{
		Summary: model.Summary{ScoreText: "40.0%"},
		Results: []model.Result{mutantResult("M-lonely", model.VerdictSurvived, "a.go", 10)},
	}
	d := Compare(baseline, current)
	if len(d.LikelyShifted) != 0 {
		t.Fatalf("fixture should have no shift candidates, got %#v", d.LikelyShifted)
	}
	var b bytes.Buffer
	if err := RenderHTML(&b, d); err != nil {
		t.Fatal(err)
	}
	out := b.String()
	if strings.Contains(out, "likely shifted") {
		t.Fatalf("an uncorrelated entry must not render a shift note at all:\n%s", out)
	}
	// A rendered (non-empty) shift note always carries class="shift-note"
	// with actual text after it; the CSS rule declaring that class in
	// <style> is present on every page regardless, so checking for the
	// bare class name would be a false positive -- check for the class
	// attribute usage (with the closing quote) instead.
	if strings.Contains(out, `class="shift-note"`) {
		t.Fatalf("an uncorrelated entry must not render a shift-note element at all:\n%s", out)
	}
}

// TestRenderHTMLEscapesUserContent confirms html/template's contextual
// auto-escaping is actually in effect: a mutation's description and
// suggestion are derived from real Go source (which routinely
// contains "<", ">", "&&") and must never be interpreted as markup.
func TestRenderHTMLEscapesUserContent(t *testing.T) {
	current := model.Report{
		Summary: model.Summary{ScoreText: "0.0%"},
		Results: []model.Result{{
			Verdict: model.VerdictSurvived,
			Mutation: model.Mutation{
				ID: "M-html", RuleID: "MJ-BOUNDARY", Operator: "boundary",
				Span:        model.Span{File: "a.go", StartLine: 1, StartCol: 1},
				Description: `replace <script>alert(1)</script> && n > 0`,
				Suggestion:  `cover n <= 0`,
			},
		}},
	}
	d := Compare(model.Report{}, current)
	var b bytes.Buffer
	if err := RenderHTML(&b, d); err != nil {
		t.Fatal(err)
	}
	out := b.String()
	if strings.Contains(out, "<script>") {
		t.Fatalf("mutation description must be HTML-escaped, not injected raw:\n%s", out)
	}
	if !strings.Contains(out, "&lt;script&gt;") {
		t.Fatalf("expected escaped script tag in output:\n%s", out)
	}
}

func TestRenderHTMLIsWellFormedDocument(t *testing.T) {
	d := Compare(model.Report{}, model.Report{})
	var b bytes.Buffer
	if err := RenderHTML(&b, d); err != nil {
		t.Fatal(err)
	}
	out := b.String()
	if !strings.HasPrefix(out, "<!doctype html>") || !strings.Contains(out, "</html>") {
		t.Fatalf("expected a complete HTML document:\n%s", out)
	}
	if !strings.Contains(out, "0 mutant IDs unchanged") {
		t.Fatalf("expected an unchanged-count sentence, got:\n%s", out)
	}
}
