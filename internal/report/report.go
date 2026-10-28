package report

import (
	"bytes"
	_ "embed"
	"encoding/json"
	"fmt"
	"html/template"
	"io"
	"strconv"
	"strings"
	"time"

	"github.com/example/mutation-judge/internal/model"
	"github.com/example/mutation-judge/internal/resultspool"
)

//go:embed report.html.tmpl
var pageSource string

var page = template.Must(template.New("report").Funcs(template.FuncMap{"join": strings.Join}).Parse(pageSource))

func Render(w io.Writer, format string, r model.Report) error {
	switch format {
	case "json":
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		return enc.Encode(r)
	case "html":
		return renderHTML(w, r)
	case "text":
		return renderText(w, r)
	case "sarif":
		return renderSARIF(w, r)
	case "github":
		return renderGitHubAnnotations(w, r)
	default:
		return fmt.Errorf("unsupported report format %q", format)
	}
}

// RenderMeasured uses a counting pass to measure serialization, then streams
// the finished report directly to the destination. The previous implementation
// accumulated the complete report in memory to patch two timing sentinels;
// for large mutation sets that duplicated the entire report's resident size.
// The dry serialization pass is excluded from TotalMS except for its measured
// rendering cost. Destination I/O is still excluded from that metric.
func RenderMeasured(w io.Writer, format string, r *model.Report) error {
	baseTotal := r.Timing.TotalMS
	started := time.Now()
	if err := Render(io.Discard, format, *r); err != nil {
		return err
	}
	r.Timing.RenderingMS = time.Since(started).Milliseconds()
	r.Timing.TotalMS = baseTotal + r.Timing.RenderingMS
	return Render(w, format, *r)
}

type errorWriter struct {
	w   io.Writer
	err error
}

func (w *errorWriter) printf(format string, args ...any) {
	if w.err != nil {
		return
	}
	_, w.err = fmt.Fprintf(w.w, format, args...)
}

func renderText(dst io.Writer, r model.Report) error {
	return renderTextIter(dst, r, func(fn func(model.Result) error) error {
		for _, x := range r.Results {
			if err := fn(x); err != nil {
				return err
			}
		}
		return nil
	})
}

func renderTextIter(dst io.Writer, r model.Report, each func(func(model.Result) error) error) error {
	status := "complete"
	if !r.Complete {
		status = "INCOMPLETE"
	}
	w := &errorWriter{w: dst}
	w.printf("mutation-judge %s (%s)\npatterns: %s\nbound: max_mutants=%v, timeout=%v\n\n", r.ToolVersion, status, strings.Join(r.Patterns, " "), r.Bounds["max_mutants"], r.Bounds["per_mutant_timeout"])
	iterErr := each(func(x model.Result) error {
		cacheMark := ""
		if x.Cached {
			cacheMark = " [cached]"
		}
		covered := "coverage: unknown"
		if x.CoverageKnown {
			if x.Covered {
				covered = "coverage: covered"
			} else {
				covered = "coverage: not covered"
			}
		}
		w.printf("%s %s %s:%d:%d %s%s\n", x.Verdict, x.Mutation.ID, x.Mutation.Span.File, x.Mutation.Span.StartLine, x.Mutation.Span.StartCol, x.Mutation.Description, cacheMark)
		w.printf("  %s\n", covered)
		if len(x.Responsible) > 0 {
			w.printf("  killed by: %s\n", strings.Join(x.Responsible, ", "))
		}
		for _, a := range x.Assertions {
			w.printf("  assertion: %s\n", formatAssertion(a))
		}
		for _, p := range x.Panics {
			w.printf("  crashed: %s: %s\n", p.Test, p.Message)
		}
		if x.Verdict == model.VerdictSurvived {
			w.printf("  suggested test: %s\n", x.Mutation.Suggestion)
			for _, line := range strings.Split(strings.TrimSuffix(x.Mutation.Diff, "\n"), "\n") {
				w.printf("    %s\n", line)
			}
		}
		if x.Verdict == model.VerdictEquivalent {
			w.printf("  proof: %s\n", x.Mutation.EquivalentReason)
		}
		return w.err
	})
	if iterErr != nil {
		return iterErr
	}
	if len(r.Warnings) > 0 {
		w.printf("\nwarnings\n")
		for _, warning := range r.Warnings {
			w.printf("  - %s\n", warning)
		}
	}
	s := r.Summary
	w.printf("\nsummary\n  %d mutants generated\n  %d killed, %d survived, %d invalid, %d timeout, %d unknown, %d unsupported, %d equivalent\n  score: %s\n", s.Generated, s.Killed, s.Survived, s.Invalid, s.Timeout, s.Unknown, s.Unsupported, s.Equivalent, s.ScoreText)
	w.printf("timing: parse=%dms baseline=%dms mutants=%dms render=%dms total=%dms\n", r.Timing.ParsingMS, r.Timing.BaselineMS, r.Timing.ExecutionMS, r.Timing.RenderingMS, r.Timing.TotalMS)
	return w.err
}

func renderHTML(w io.Writer, r model.Report) error { return page.Execute(w, r) }

// formatAssertion renders one responsible assertion for the text report.
func formatAssertion(a model.Assertion) string {
	var b strings.Builder
	if a.Test != "" {
		b.WriteString(a.Test)
		b.WriteString(": ")
	}
	if a.File != "" {
		b.WriteString(a.File)
		if a.Line > 0 {
			b.WriteByte(':')
			b.WriteString(strconv.Itoa(a.Line))
		}
		b.WriteString(": ")
	}
	b.WriteString(a.Message)
	return b.String()
}

// RenderSpoolMeasured renders an ordered on-disk result stream without
// constructing model.Report.Results or allocating a whole report buffer.
// Only JSON and text have streaming implementations; other formats use
// the in-memory API until a separate streaming renderer is added.
func RenderSpoolMeasured(w io.Writer, format string, r *model.Report, spool *resultspool.Spool) error {
	if format != "json" && format != "text" {
		return fmt.Errorf("streamed results require json or text format (got %q)", format)
	}
	baseTotal := r.Timing.TotalMS
	started := time.Now()
	if err := renderSpool(io.Discard, format, *r, spool); err != nil {
		return err
	}
	r.Timing.RenderingMS = time.Since(started).Milliseconds()
	r.Timing.TotalMS = baseTotal + r.Timing.RenderingMS
	return renderSpool(w, format, *r, spool)
}

func renderSpool(w io.Writer, format string, r model.Report, spool *resultspool.Spool) error {
	if format == "text" {
		return renderTextIter(w, r, spool.ForEach)
	}
	if format != "json" {
		return fmt.Errorf("unsupported streaming format %q", format)
	}
	// Marshal only small report metadata. The empty results array provides a
	// unique well-formed insertion point without serializing any result twice.
	r.Results = []model.Result{}
	metadata, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return err
	}
	marker := []byte("\n  \"results\": []")
	idx := bytes.Index(metadata, marker)
	if idx < 0 {
		return fmt.Errorf("missing results field in report metadata")
	}
	split := idx + len(marker) - 2 // replace exactly the empty [] value
	if _, err := w.Write(metadata[:split]); err != nil {
		return err
	}
	if _, err := io.WriteString(w, "[\n"); err != nil {
		return err
	}
	first := true
	err = spool.ForEach(func(r model.Result) error {
		if !first {
			if _, err := io.WriteString(w, ",\n"); err != nil {
				return err
			}
		}
		first = false
		data, err := json.MarshalIndent(r, "    ", "  ")
		if err != nil {
			return err
		}
		if _, err := io.WriteString(w, "    "); err != nil {
			return err
		}
		_, err = w.Write(data)
		return err
	})
	if err != nil {
		return err
	}
	if _, err := io.WriteString(w, "\n  ]"); err != nil {
		return err
	}
	if _, err := w.Write(metadata[split+2:]); err != nil {
		return err
	}
	_, err = io.WriteString(w, "\n")
	return err
}
