package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"

	"github.com/example/mutation-judge/internal/compare"
	"github.com/example/mutation-judge/internal/model"
)

func runCompare(args []string) int {
	fs := flag.NewFlagSet("mutation-judge compare", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	baselinePath := fs.String("baseline", "", "baseline report JSON file (e.g. the base branch's report); required")
	currentPath := fs.String("current", "", "current report JSON file (e.g. the pull request's report); required")
	format := fs.String("format", "text", "output format: text or json")
	output := fs.String("output", "", "write output to this path instead of stdout")
	failOnNew := fs.Bool("fail-on-new-survivors", false, "exit with --fail-exit-code if any new survivors are found")
	failExitCode := fs.Int("fail-exit-code", 10, "exit code used when --fail-on-new-survivors triggers")
	fs.Usage = func() {
		fmt.Fprintln(fs.Output(), "Usage: mutation-judge compare --baseline old.json --current new.json [flags]")
		fmt.Fprintln(fs.Output(), "Diffs two --format json reports at the mutant level into four buckets: new\nsurvivors (newly actionable -- SURVIVED, TIMEOUT, or UNKNOWN), fixed survivors\n(still present, no longer actionable), removed mutants (no longer present at\nall, regardless of prior verdict), and an unchanged count. Matching is by\nmutant ID, which hashes each mutant's file and byte offset -- an edit\nanywhere earlier in a file shifts every later mutant's ID even if that\nmutation site itself didn't change; see docs/limitations.md before relying on\nthis across a large, heavily-edited file.")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return exitOK
		}
		return exitInput
	}
	if *baselinePath == "" || *currentPath == "" {
		fmt.Fprintln(os.Stderr, "compare: both --baseline and --current are required")
		return exitInput
	}
	baseline, err := loadReport(*baselinePath)
	if err != nil {
		fmt.Fprintln(os.Stderr, "compare:", err)
		return exitInput
	}
	current, err := loadReport(*currentPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, "compare:", err)
		return exitInput
	}
	d := compare.Compare(baseline, current)

	dst := os.Stdout
	var f *os.File
	if *output != "" {
		if err := os.MkdirAll(parentDir(*output), 0o755); err != nil {
			fmt.Fprintln(os.Stderr, err)
			return exitInput
		}
		f, err = os.Create(*output)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return exitInput
		}
		dst = f
	}
	var renderErr error
	switch *format {
	case "text":
		renderErr = compare.RenderText(dst, d)
	case "json":
		renderErr = compare.RenderJSON(dst, d)
	default:
		renderErr = fmt.Errorf("unsupported format %q", *format)
	}
	if f != nil {
		if closeErr := f.Close(); renderErr == nil {
			renderErr = closeErr
		}
	}
	if renderErr != nil {
		fmt.Fprintln(os.Stderr, "compare:", renderErr)
		return exitInternal
	}
	if *failOnNew && len(d.NewSurvivors) > 0 {
		return *failExitCode
	}
	return exitOK
}

// loadReport reads a --format json report previously written by this
// same tool. compare, record, and trend all read reports this way
// rather than re-running analysis themselves -- they're pure
// post-processing over whatever the caller already produced.
func loadReport(path string) (model.Report, error) {
	f, err := os.Open(path)
	if err != nil {
		return model.Report{}, fmt.Errorf("open %s: %w", path, err)
	}
	defer f.Close()
	var r model.Report
	if err := json.NewDecoder(f).Decode(&r); err != nil {
		return model.Report{}, fmt.Errorf("decode %s: %w", path, err)
	}
	return r, nil
}
