package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// journalEntry is one line of the append-only interruption journal. It
// exists specifically for post-mortem debugging beyond the exit code and
// whatever partial report was already written: a person whose CI wraps
// this tool and only surfaces the exit code (or who wasn't watching when
// it happened) still has a durable, greppable record of when and why a
// run was cut short. This is independent of the result cache -- it's
// written regardless of --no-cache.
type journalEntry struct {
	Time             time.Time `json:"time"`
	Signal           string    `json:"signal"`
	Phase            string    `json:"phase"` // "baseline" or "mutants"
	ToolVersion      string    `json:"tool_version"`
	Patterns         []string  `json:"patterns"`
	Operators        []string  `json:"operators"`
	CompletedMutants int       `json:"completed_mutants,omitempty"`
	RetainedMutants  int       `json:"retained_mutants,omitempty"`
	ExitCode         int       `json:"exit_code"`
}

const journalPath = ".mutation-judge/journal.ndjson"

// appendJournalEntry appends one NDJSON line to the interruption journal,
// creating the parent directory and file on first use. A failure here is
// deliberately non-fatal and does not change the process exit code --
// failing to log an interruption should not itself produce a worse
// failure mode than the interruption already is -- but it is still
// surfaced on stderr rather than silently dropped, matching how a
// non-fatal cache write failure is handled elsewhere in this tool.
func appendJournalEntry(cwd string, entry journalEntry) error {
	path := filepath.Join(cwd, journalPath)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create journal directory: %w", err)
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return fmt.Errorf("open journal: %w", err)
	}
	defer f.Close()
	b, err := json.Marshal(entry)
	if err != nil {
		return fmt.Errorf("encode journal entry: %w", err)
	}
	if _, err := f.Write(append(b, '\n')); err != nil {
		return fmt.Errorf("write journal entry: %w", err)
	}
	return nil
}
