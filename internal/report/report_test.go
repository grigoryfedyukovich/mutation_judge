package report

import (
	"bytes"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/example/mutation-judge/internal/model"
)

func TestTextIncludesSuggestion(t *testing.T) {
	r := model.Report{ToolVersion: "x", Complete: true, Patterns: []string{"."}, Bounds: map[string]any{"max_mutants": 0, "per_mutant_timeout": "1s"}, Summary: model.Summary{Generated: 1, Survived: 1, ScoreText: "0.0%"}, Results: []model.Result{{Verdict: model.VerdictSurvived, Mutation: model.Mutation{ID: "M-1", Span: model.Span{File: "a.go", StartLine: 1, StartCol: 1}, Description: "change", Suggestion: "test zero", Diff: "diff"}}}}
	var b bytes.Buffer
	if err := Render(&b, "text", r); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(b.String(), "suggested test: test zero") {
		t.Fatal(b.String())
	}
}

func TestTextIncludesEquivalentProof(t *testing.T) {
	r := model.Report{
		ToolVersion: "x", Complete: true, Patterns: []string{"."}, Bounds: map[string]any{"max_mutants": 0, "per_mutant_timeout": "1s"},
		Summary: model.Summary{Generated: 1, Equivalent: 1, ScoreText: "n/a (no scoreable mutants)"},
		Results: []model.Result{{
			Verdict: model.VerdictEquivalent,
			Mutation: model.Mutation{
				ID: "M-1", Span: model.Span{File: "a.go", StartLine: 1, StartCol: 1},
				Description: "replace comparison < with <=", EquivalentReason: "dominated by the enclosing guard \"a.X != b.X\"",
			},
		}},
	}
	var b bytes.Buffer
	if err := Render(&b, "text", r); err != nil {
		t.Fatal(err)
	}
	out := b.String()
	if !strings.Contains(out, "EQUIVALENT M-1") {
		t.Fatalf("expected the EQUIVALENT verdict line: %s", out)
	}
	if !strings.Contains(out, `proof: dominated by the enclosing guard "a.X != b.X"`) {
		t.Fatalf("expected the proof line, got: %s", out)
	}
	if strings.Contains(out, "suggested test:") {
		t.Fatalf("an EQUIVALENT result has no suggestion to print: %s", out)
	}
	if !strings.Contains(out, "1 equivalent") {
		t.Fatalf("expected the summary line to count equivalent mutants: %s", out)
	}
}

func TestTextGolden(t *testing.T) {
	r := model.Report{
		ToolVersion: "test", Complete: true, Patterns: []string{"./pkg"},
		Bounds:  map[string]any{"max_mutants": 1, "per_mutant_timeout": "2s"},
		Summary: model.Summary{Generated: 1, Survived: 1, ScoreText: "0.0% excluding invalid/timeout"},
		Timing:  model.Timing{ParsingMS: 1, BaselineMS: 2, ExecutionMS: 3, TotalMS: 7},
		Results: []model.Result{{
			Verdict: model.VerdictSurvived, Covered: true, CoverageKnown: true,
			Mutation: model.Mutation{
				ID: "M-demo", Span: model.Span{File: "pkg/demo.go", StartLine: 3, StartCol: 5},
				Description: "replace comparison > with >=", Suggestion: "exercise equality",
				Diff: "--- a/pkg/demo.go\n+++ b/pkg/demo.go\n@@ -3,1 +3,1 @@\n-\tif n > 0 {\n+\tif n >= 0 {\n",
			},
		}},
	}
	var b bytes.Buffer
	if err := Render(&b, "text", r); err != nil {
		t.Fatal(err)
	}
	want, err := os.ReadFile(filepath.Join("..", "..", "tests", "golden", "text_report.golden"))
	if err != nil {
		t.Fatal(err)
	}
	if b.String() != string(want) {
		t.Fatalf("golden mismatch\n--- got ---\n%s\n--- want ---\n%s", b.String(), want)
	}
}

func TestTextPropagatesLateWriterFailure(t *testing.T) {
	wantErr := errors.New("disk full")
	w := &failAfterWriter{remaining: 80, err: wantErr}
	r := model.Report{ToolVersion: "x", Complete: true, Patterns: []string{"."}, Bounds: map[string]any{"max_mutants": 0, "per_mutant_timeout": "1s"}, Summary: model.Summary{ScoreText: "n/a"}}
	if err := Render(w, "text", r); !errors.Is(err, wantErr) {
		t.Fatalf("got %v, want %v", err, wantErr)
	}
}

func TestHTMLReportRendersAndEscapesSource(t *testing.T) {
	r := model.Report{
		ToolVersion: "x", Complete: false, Patterns: []string{"."}, Bounds: map[string]any{},
		Summary: model.Summary{Generated: 1, Survived: 1, ScoreText: "0%"},
		Results: []model.Result{{Verdict: model.VerdictSurvived, Mutation: model.Mutation{ID: "M-1", Description: "x < y", Diff: "-x < y\n+x <= y\n"}}},
	}
	var b bytes.Buffer
	if err := Render(&b, "html", r); err != nil {
		t.Fatal(err)
	}
	text := b.String()
	if !strings.Contains(text, "INCOMPLETE REPORT") || !strings.Contains(text, "x &lt; y") {
		t.Fatalf("unexpected HTML:\n%s", text)
	}
}

func TestRenderMeasuredPatchesTimingWithoutSecondRender(t *testing.T) {
	r := model.Report{
		ToolVersion: "x", Complete: true, Patterns: []string{"."}, Bounds: map[string]any{}, Summary: model.Summary{ScoreText: "n/a"}, Timing: model.Timing{TotalMS: 7},
		Results: []model.Result{{Mutation: model.Mutation{Diff: "user source -9223372036854775807 must remain"}}},
	}
	var b bytes.Buffer
	if err := RenderMeasured(&b, "json", &r); err != nil {
		t.Fatal(err)
	}
	if r.Timing.TotalMS < 7 || strings.Contains(b.String(), `"rendering_ms": -9223372036854775807`) || strings.Contains(b.String(), `"total_ms": -9223372036854775806`) {
		t.Fatalf("timing was not patched: %#v\n%s", r.Timing, b.String())
	}
	if !strings.Contains(b.String(), `"rendering_ms":`) {
		t.Fatalf("missing rendering timing: %s", b.String())
	}
	if !strings.Contains(b.String(), "user source -9223372036854775807 must remain") {
		t.Fatalf("source content was altered while patching timing: %s", b.String())
	}
}

type failAfterWriter struct {
	remaining int
	err       error
}

func (w *failAfterWriter) Write(p []byte) (int, error) {
	if w.remaining <= 0 {
		return 0, w.err
	}
	if len(p) > w.remaining {
		n := w.remaining
		w.remaining = 0
		return n, w.err
	}
	w.remaining -= len(p)
	return len(p), nil
}

var _ io.Writer = (*failAfterWriter)(nil)
