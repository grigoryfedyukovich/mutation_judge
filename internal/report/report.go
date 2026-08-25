package report

import (
	"bytes"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"io"
	"strconv"
	"strings"
	"time"

	"github.com/example/mutation-judge/internal/model"
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

// RenderMeasured serializes the report once, records that serialization cost,
// patches the two reserved timing values in the serialized bytes, and then
// emits the finished artifact. Destination I/O is intentionally not included
// in rendering_ms because it depends on the caller's output device.
func RenderMeasured(w io.Writer, format string, r *model.Report) error {
	const renderSentinel int64 = -9223372036854775807
	const totalSentinel int64 = -9223372036854775806

	baseTotal := r.Timing.TotalMS
	toRender := *r
	toRender.Timing.RenderingMS = renderSentinel
	toRender.Timing.TotalMS = totalSentinel

	var buf bytes.Buffer
	started := time.Now()
	if err := Render(&buf, format, toRender); err != nil {
		return err
	}
	renderingMS := time.Since(started).Milliseconds()
	r.Timing.RenderingMS = renderingMS
	r.Timing.TotalMS = baseTotal + renderingMS

	out, ok := replaceLast(buf.Bytes(), strconv.FormatInt(renderSentinel, 10), strconv.FormatInt(r.Timing.RenderingMS, 10))
	if !ok {
		return errors.New("rendering timing placeholder missing from serialized report")
	}
	out, ok = replaceLast(out, strconv.FormatInt(totalSentinel, 10), strconv.FormatInt(r.Timing.TotalMS, 10))
	if !ok {
		return errors.New("total timing placeholder missing from serialized report")
	}
	_, err := w.Write(out)
	return err
}

func replaceLast(data []byte, old, replacement string) ([]byte, bool) {
	i := bytes.LastIndex(data, []byte(old))
	if i < 0 {
		return data, false
	}
	out := make([]byte, 0, len(data)-len(old)+len(replacement))
	out = append(out, data[:i]...)
	out = append(out, replacement...)
	out = append(out, data[i+len(old):]...)
	return out, true
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
	status := "complete"
	if !r.Complete {
		status = "INCOMPLETE"
	}
	w := &errorWriter{w: dst}
	w.printf("mutation-judge %s (%s)\npatterns: %s\nbound: max_mutants=%v, timeout=%v\n\n", r.ToolVersion, status, strings.Join(r.Patterns, " "), r.Bounds["max_mutants"], r.Bounds["per_mutant_timeout"])
	for _, x := range r.Results {
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
		if x.Verdict == model.VerdictSurvived {
			w.printf("  suggested test: %s\n", x.Mutation.Suggestion)
			for _, line := range strings.Split(strings.TrimSuffix(x.Mutation.Diff, "\n"), "\n") {
				w.printf("    %s\n", line)
			}
		}
		if x.Verdict == model.VerdictEquivalent {
			w.printf("  proof: %s\n", x.Mutation.EquivalentReason)
		}
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
