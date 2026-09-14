package compare

import (
	_ "embed"
	"html/template"
	"io"
)

//go:embed compare.html.tmpl
var comparePageSource string

var comparePage = template.Must(template.New("compare").Parse(comparePageSource))

// htmlData adapts a Diff for compare.html.tmpl. html/template has no
// way to build a map from LikelyShifted itself, so the same
// byRemovedID/byNewID lookups RenderText builds in Go (see render.go)
// are precomputed here too, under the same by-ID keying.
//
// The values are pointers, not ShiftCandidate values, on purpose: the
// template looks an entry up with `{{with index $.ShiftByNewID .ID}}`
// to decide whether to print a shift note at all. text/template's
// `with` treats a struct as truthy unconditionally, regardless of its
// field values -- there is no such thing as an "empty" struct to it --
// so a plain map[string]ShiftCandidate would make `index` return a
// zero-value ShiftCandidate{} for every ID that has no correlation,
// and `with` would still render the (blank) shift note on every
// single entry. A map of pointers doesn't have this problem: a missing
// key's zero value is a nil pointer, which `with`/`index` do treat as
// empty, so the note only renders for IDs that are actually present.
type htmlData struct {
	Diff
	ShiftByNewID     map[string]*ShiftCandidate
	ShiftByRemovedID map[string]*ShiftCandidate
}

// RenderHTML writes a self-contained HTML page visualizing a Diff: the
// same six buckets and likely-shifted correlations RenderText prints
// to a terminal, as a browsable report instead. Verdicts are
// colour-coded with the same classes report.html.tmpl uses for a
// single report, so the two look like one family rather than two
// unrelated tools.
func RenderHTML(w io.Writer, d Diff) error {
	data := htmlData{
		Diff:             d,
		ShiftByNewID:     map[string]*ShiftCandidate{},
		ShiftByRemovedID: map[string]*ShiftCandidate{},
	}
	for i := range d.LikelyShifted {
		sc := &d.LikelyShifted[i]
		data.ShiftByRemovedID[sc.RemovedID] = sc
		data.ShiftByNewID[sc.NewID] = sc
	}
	return comparePage.Execute(w, data)
}
