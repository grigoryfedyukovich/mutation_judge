package report

import (
	"bytes"
	"strings"
	"testing"

	"github.com/example/mutation-judge/internal/model"
)

func TestGitHubOnlyAnnotatesActionableVerdicts(t *testing.T) {
	var b bytes.Buffer
	if err := Render(&b, "github", sampleReportForFormats()); err != nil {
		t.Fatal(err)
	}
	out := b.String()
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")

	warnings, notices, debugs := 0, 0, 0
	for _, line := range lines {
		switch {
		case strings.HasPrefix(line, "::warning "):
			warnings++
			if !strings.Contains(line, "file=pkg/demo.go") || !strings.Contains(line, "line=3") {
				t.Fatalf("survived annotation missing location: %s", line)
			}
			if !strings.Contains(line, "mutation survived: replace comparison") {
				t.Fatalf("survived annotation missing message: %s", line)
			}
		case strings.HasPrefix(line, "::notice title=mutation-judge summary::"):
			notices++
		case strings.HasPrefix(line, "::notice "):
			// the timeout finding's own ::notice:: line
			if !strings.Contains(line, "file=pkg/loop.go") {
				t.Fatalf("timeout annotation missing location: %s", line)
			}
		case strings.HasPrefix(line, "::debug::"):
			debugs++
			if !strings.Contains(line, "rendering_ms=") || !strings.Contains(line, "total_ms=") {
				t.Fatalf("debug line missing timing fields: %s", line)
			}
		}
	}
	if warnings != 1 {
		t.Fatalf("expected exactly 1 ::warning:: line (the survived mutant), got %d in:\n%s", warnings, out)
	}
	if notices != 1 {
		t.Fatalf("expected exactly 1 summary ::notice:: line, got %d in:\n%s", notices, out)
	}
	if debugs != 1 {
		t.Fatalf("expected exactly 1 ::debug:: timing line, got %d in:\n%s", debugs, out)
	}
	if strings.Contains(out, "M-killed") || strings.Contains(out, "M-invalid") {
		t.Fatalf("KILLED/INVALID mutants must not produce annotations:\n%s", out)
	}
}

func TestGitHubCleanRunStillProducesSummaryLine(t *testing.T) {
	r := sampleReportForFormats()
	r.Results = r.Results[1:2] // killed only
	var b bytes.Buffer
	if err := Render(&b, "github", r); err != nil {
		t.Fatal(err)
	}
	out := b.String()
	if strings.Contains(out, "::warning ") {
		t.Fatalf("a clean run must not emit any ::warning:: lines:\n%s", out)
	}
	if !strings.Contains(out, "::notice title=mutation-judge summary::") {
		t.Fatalf("a clean run must still emit a summary line so the log isn't silent:\n%s", out)
	}
}

func TestGitHubEscapesMessageAndPropertyText(t *testing.T) {
	r := model.Report{
		ToolVersion: "test", Complete: true, Bounds: map[string]any{}, Summary: model.Summary{ScoreText: "n/a"},
		Results: []model.Result{{
			Verdict: model.VerdictSurvived,
			Mutation: model.Mutation{
				ID: "M-1", RuleID: "MJ-BOUNDARY",
				Span:        model.Span{File: "weird,file:name.go", StartLine: 1},
				Description: "100% survived\nwith a newline",
			},
		}},
	}
	var b bytes.Buffer
	if err := Render(&b, "github", r); err != nil {
		t.Fatal(err)
	}
	out := b.String()
	if strings.Contains(out, "weird,file:name.go") {
		t.Fatalf("unescaped comma/colon in file= property will corrupt annotation parsing:\n%s", out)
	}
	if !strings.Contains(out, "file=weird%2Cfile%3Aname.go") {
		t.Fatalf("expected escaped file property, got:\n%s", out)
	}
	if strings.Contains(out, "100%25%25") {
		t.Fatalf("percent sign must be escaped exactly once:\n%s", out)
	}
	if !strings.Contains(out, "100%25 survived%0Awith a newline") {
		t.Fatalf("expected escaped %% and newline in message text:\n%s", out)
	}
}
