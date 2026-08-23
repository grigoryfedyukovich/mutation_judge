package report

import (
	"io"
	"strconv"
	"strings"

	"github.com/example/mutation-judge/internal/model"
)

// githubLevel maps a verdict to a GitHub Actions workflow-command
// annotation level. Inclusion policy matches sarifIncluded exactly --
// see the comment there for why KILLED/INVALID/UNSUPPORTED never
// produce an annotation.
func githubLevel(v model.Verdict) (command string, ok bool) {
	switch v {
	case model.VerdictSurvived:
		return "warning", true
	case model.VerdictTimeout, model.VerdictUnknown:
		return "notice", true
	default:
		return "", false
	}
}

// renderGitHubAnnotations writes one GitHub Actions workflow command
// per actionable finding (see GitHub's "workflow commands for GitHub
// Actions" docs), so CI usage surfaces survivors as inline PR
// annotations with no SARIF upload step required. A trailing
// ::notice:: line always summarizes the run, even when there's
// nothing to flag, so a clean run is still visible in the log rather
// than producing no output at all.
func renderGitHubAnnotations(w io.Writer, r model.Report) error {
	ew := &errorWriter{w: w}
	count := 0
	for _, x := range r.Results {
		command, ok := githubLevel(x.Verdict)
		if !ok {
			continue
		}
		count++
		m := x.Mutation
		msg := findingMessage(x.Verdict, m.Description, m.Suggestion)
		params := []string{
			"file=" + ghEscapeProperty(m.Span.File),
			"line=" + strconv.Itoa(m.Span.StartLine),
		}
		if m.Span.EndLine > 0 {
			params = append(params, "endLine="+strconv.Itoa(m.Span.EndLine))
		}
		if m.Span.StartCol > 0 {
			params = append(params, "col="+strconv.Itoa(m.Span.StartCol))
		}
		params = append(params, "title="+ghEscapeProperty(m.RuleID+" "+m.ID))
		ew.printf("::%s %s::%s\n", command, strings.Join(params, ","), ghEscapeData(msg))
	}
	s := r.Summary
	ew.printf("::notice title=mutation-judge summary::%s (%d killed, %d survived, %d invalid, %d timeout, %d unknown, %d unsupported, %d flagged)\n",
		ghEscapeData(s.ScoreText), s.Killed, s.Survived, s.Invalid, s.Timeout, s.Unknown, s.Unsupported, count)
	// See sarif.go's equivalent comment: RenderMeasured patches these
	// two sentinels into the serialized output after timing the render
	// itself, so they need to be present somewhere in the output for
	// every format, not just the structured ones. ::debug:: commands
	// are silent unless the workflow has step debug logging enabled,
	// so this doesn't add noise to an ordinary CI log.
	ew.printf("::debug::mutation-judge timing rendering_ms=%d total_ms=%d\n", r.Timing.RenderingMS, r.Timing.TotalMS)
	return ew.err
}

// ghEscapeData escapes workflow-command message/data text per
// GitHub's documented rules: %, CR, and LF.
func ghEscapeData(s string) string {
	s = strings.ReplaceAll(s, "%", "%25")
	s = strings.ReplaceAll(s, "\r", "%0D")
	s = strings.ReplaceAll(s, "\n", "%0A")
	return s
}

// ghEscapeProperty escapes workflow-command property values: the same
// three characters as data, plus : and , since those delimit
// properties themselves.
func ghEscapeProperty(s string) string {
	s = ghEscapeData(s)
	s = strings.ReplaceAll(s, ":", "%3A")
	s = strings.ReplaceAll(s, ",", "%2C")
	return s
}
