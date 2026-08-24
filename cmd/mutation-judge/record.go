package main

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/example/mutation-judge/internal/history"
)

func runRecord(args []string) int {
	fs := flag.NewFlagSet("mutation-judge record", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	label := fs.String("label", "", "label for this entry (a PR number, branch, or commit SHA); required")
	historyPath := fs.String("history-file", history.DefaultPath, "NDJSON history log to append to")
	fs.Usage = func() {
		fmt.Fprintln(fs.Output(), "Usage: mutation-judge record <report.json> --label <label> [flags]")
		fmt.Fprintln(fs.Output(), "Appends one entry to the score-history log for later use by `mutation-judge\ntrend`. Run once per CI job, right after the normal analysis command,\npointing at the --format json report it just wrote:\n\n  mutation-judge --format json --output report.json ./...\n  mutation-judge record report.json --label \"PR #104\"")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return exitOK
		}
		return exitInput
	}
	if *label == "" {
		fmt.Fprintln(os.Stderr, "record: --label is required")
		return exitInput
	}
	reportArgs := fs.Args()
	if len(reportArgs) != 1 {
		fmt.Fprintln(os.Stderr, "record: expected exactly one report JSON file argument")
		return exitInput
	}
	r, err := loadReport(reportArgs[0])
	if err != nil {
		fmt.Fprintln(os.Stderr, "record:", err)
		return exitInput
	}
	entry := history.EntryFromReport(*label, r, time.Now().UTC())
	if err := history.Append(*historyPath, entry); err != nil {
		fmt.Fprintln(os.Stderr, "record:", err)
		return exitInternal
	}
	return exitOK
}
