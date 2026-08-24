// Package history persists one score-summary entry per Mutation Judge
// run to a local NDJSON log, so a caller (typically CI, once per PR or
// commit) can later render a trend of scores over time. It mirrors
// cmd/mutation-judge/journal.go's append-only NDJSON pattern, kept as
// its own package because unlike the interruption journal this one is
// also read back, not just appended and left for a human to grep.
package history

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/example/mutation-judge/internal/model"
)

// DefaultPath is where the history log lives by default, alongside
// the result cache and interruption journal.
const DefaultPath = ".mutation-judge/history.ndjson"

// Entry is one row: one run's score summary, tagged with a
// caller-supplied label -- a PR number, commit SHA, or anything else
// the caller's CI already has on hand. Mutation Judge does not invent
// or look up this label itself; there is no single correct source for
// it (PR number, branch name, and commit SHA are all reasonable, and
// which one is meaningful is a property of the caller's CI, not of a
// report).
type Entry struct {
	Time        time.Time `json:"time"`
	Label       string    `json:"label"`
	Score       float64   `json:"score"`
	ScoreText   string    `json:"score_text"`
	Generated   int       `json:"generated"`
	Killed      int       `json:"killed"`
	Survived    int       `json:"survived"`
	Invalid     int       `json:"invalid"`
	Timeout     int       `json:"timeout"`
	Unknown     int       `json:"unknown"`
	Unsupported int       `json:"unsupported"`
}

// EntryFromReport builds an Entry from a report's own summary.
func EntryFromReport(label string, r model.Report, t time.Time) Entry {
	s := r.Summary
	return Entry{
		Time: t, Label: label, Score: s.Score, ScoreText: s.ScoreText,
		Generated: s.Generated, Killed: s.Killed, Survived: s.Survived,
		Invalid: s.Invalid, Timeout: s.Timeout, Unknown: s.Unknown, Unsupported: s.Unsupported,
	}
}

// Append adds one entry to the NDJSON history log at path, creating
// the parent directory and file on first use.
func Append(path string, e Entry) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create history directory: %w", err)
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return fmt.Errorf("open history: %w", err)
	}
	defer f.Close()
	b, err := json.Marshal(e)
	if err != nil {
		return fmt.Errorf("encode history entry: %w", err)
	}
	if _, err := f.Write(append(b, '\n')); err != nil {
		return fmt.Errorf("write history entry: %w", err)
	}
	return nil
}

// Read reads every entry from the NDJSON history log at path, in file
// order (oldest first, so long as Append was always used to write
// it). A missing file is not an error -- it just means no history has
// been recorded yet -- but a malformed line is, since a silently
// dropped line would make a trend look like a gap in history rather
// than a corrupt file.
func Read(path string) ([]Entry, error) {
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("open history: %w", err)
	}
	defer f.Close()

	var entries []Entry
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	lineNum := 0
	for sc.Scan() {
		lineNum++
		line := bytes.TrimSpace(sc.Bytes())
		if len(line) == 0 {
			continue
		}
		var e Entry
		if err := json.Unmarshal(line, &e); err != nil {
			return nil, fmt.Errorf("history line %d: %w", lineNum, err)
		}
		entries = append(entries, e)
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("read history: %w", err)
	}
	return entries, nil
}

// RenderTrend writes one "label   score%" line per entry, in the
// order given (oldest first if entries came straight from Read),
// column-aligned to the longest label.
func RenderTrend(w io.Writer, entries []Entry) error {
	width := 0
	for _, e := range entries {
		if len(e.Label) > width {
			width = len(e.Label)
		}
	}
	ew := &errWriter{w: w}
	for _, e := range entries {
		ew.printf("%-*s  %.1f%%\n", width, e.Label, e.Score)
	}
	return ew.err
}

// RenderTrendJSON writes the entries as a JSON array, for scripting.
func RenderTrendJSON(w io.Writer, entries []Entry) error {
	if entries == nil {
		entries = []Entry{}
	}
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(entries)
}

type errWriter struct {
	w   io.Writer
	err error
}

func (e *errWriter) printf(format string, args ...any) {
	if e.err != nil {
		return
	}
	_, e.err = fmt.Fprintf(e.w, format, args...)
}
