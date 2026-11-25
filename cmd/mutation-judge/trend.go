package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
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

	openErr, writeErr := writeOutput(*output, func(dst io.Writer) error {
		switch *format {
		case "text":
			return history.RenderTrend(dst, entries)
		case "json":
			return history.RenderTrendJSON(dst, entries)
		default:
			return fmt.Errorf("unsupported format %q", *format)
		}
	})
	if openErr != nil {
		fmt.Fprintln(os.Stderr, openErr)
		return exitInput
	}
	if writeErr != nil {
		fmt.Fprintln(os.Stderr, "trend:", writeErr)
		return exitInternal
	}
	return exitOK
}
