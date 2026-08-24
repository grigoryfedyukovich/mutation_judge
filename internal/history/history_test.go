package history

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/example/mutation-judge/internal/model"
)

func TestAppendThenReadRoundTrips(t *testing.T) {
	path := filepath.Join(t.TempDir(), "history.ndjson")
	entries := []Entry{
		{Time: time.Now().UTC(), Label: "PR #101", Score: 71.2, ScoreText: "71.2%"},
		{Time: time.Now().UTC(), Label: "PR #102", Score: 74.8, ScoreText: "74.8%"},
	}
	for _, e := range entries {
		if err := Append(path, e); err != nil {
			t.Fatal(err)
		}
	}
	got, err := Read(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].Label != "PR #101" || got[1].Label != "PR #102" {
		t.Fatalf("unexpected entries: %#v", got)
	}
}

func TestReadMissingFileIsNotAnError(t *testing.T) {
	path := filepath.Join(t.TempDir(), "does-not-exist.ndjson")
	entries, err := Read(path)
	if err != nil {
		t.Fatalf("missing history file should not be an error, got: %v", err)
	}
	if entries != nil {
		t.Fatalf("expected nil entries, got %#v", entries)
	}
}

func TestReadMalformedLineIsAnError(t *testing.T) {
	path := filepath.Join(t.TempDir(), "history.ndjson")
	if err := os.WriteFile(path, []byte("{not json}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Read(path); err == nil {
		t.Fatal("expected an error for a malformed history line")
	}
}

func TestEntryFromReportCopiesSummaryFields(t *testing.T) {
	r := model.Report{Summary: model.Summary{Score: 87.5, ScoreText: "87.5%", Killed: 7, Survived: 1}}
	now := time.Now().UTC()
	e := EntryFromReport("PR #104", r, now)
	if e.Label != "PR #104" || e.Score != 87.5 || e.Killed != 7 || e.Survived != 1 || !e.Time.Equal(now) {
		t.Fatalf("unexpected entry: %#v", e)
	}
}

func TestRenderTrendMatchesRequestedShape(t *testing.T) {
	entries := []Entry{
		{Label: "PR #101", Score: 71.2},
		{Label: "PR #102", Score: 74.8},
		{Label: "PR #103", Score: 74.8},
		{Label: "PR #104", Score: 77.1},
	}
	var b bytes.Buffer
	if err := RenderTrend(&b, entries); err != nil {
		t.Fatal(err)
	}
	out := b.String()
	for _, want := range []string{"PR #101  71.2%", "PR #102  74.8%", "PR #103  74.8%", "PR #104  77.1%"} {
		if !strings.Contains(out, want) {
			t.Fatalf("missing %q in:\n%s", want, out)
		}
	}
}

func TestRenderTrendJSONEmptyIsArrayNotNull(t *testing.T) {
	var b bytes.Buffer
	if err := RenderTrendJSON(&b, nil); err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(b.String()) != "[]" {
		t.Fatalf("expected an empty array, got: %s", b.String())
	}
}
