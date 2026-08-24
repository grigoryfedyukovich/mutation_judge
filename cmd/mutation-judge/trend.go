package main

import (
	"errors"
	"flag"
	"fmt"
	"os"

	"github.com/example/mutation-judge/internal/history"
)

func runTrend(args []string) int {
	fs := flag.NewFlagSet("mutation-judge trend", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	historyPath := fs.String("history-file", history.DefaultPath, "NDJSON history log to read")
	format := fs.String("format", "text", "output format: text or json")
	output := fs.String("output", "", "write output to this path instead of stdout")
	fs.Usage = func() {
		fmt.Fprintln(fs.Output(), "Usage: mutation-judge trend [flags]")
		fmt.Fprintln(fs.Output(), "Prints the recorded score history (see `mutation-judge record`), oldest\nentry first. An empty or missing history file prints nothing (text) or []\n(json), not an error.")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return exitOK
		}
		return exitInput
	}
	entries, err := history.Read(*historyPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, "trend:", err)
		return exitInput
	}

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
		renderErr = history.RenderTrend(dst, entries)
	case "json":
		renderErr = history.RenderTrendJSON(dst, entries)
	default:
		renderErr = fmt.Errorf("unsupported format %q", *format)
	}
	if f != nil {
		if closeErr := f.Close(); renderErr == nil {
			renderErr = closeErr
		}
	}
	if renderErr != nil {
		fmt.Fprintln(os.Stderr, "trend:", renderErr)
		return exitInternal
	}
	return exitOK
}
