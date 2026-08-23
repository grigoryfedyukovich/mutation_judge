package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"runtime/debug"
	"strings"
	"syscall"
	"time"

	"github.com/example/mutation-judge/internal/analysis"
	"github.com/example/mutation-judge/internal/config"
	"github.com/example/mutation-judge/internal/model"
	"github.com/example/mutation-judge/internal/report"
)

const version = "0.1.3"
const (
	exitOK          = 0
	exitInput       = 2
	exitInternal    = 3
	exitInterrupted = 130 // matches the conventional 128+SIGINT shell exit code
)

func main() { os.Exit(run()) }

func run() (code int) {
	defer func() {
		if r := recover(); r != nil {
			fmt.Fprintf(os.Stderr, "mutation-judge internal error: %v\nreproduce with: mutation-judge %s\nversion: %s\n%s", r, strings.Join(os.Args[1:], " "), version, debug.Stack())
			code = exitInternal
		}
	}()
	cwd, err := os.Getwd()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return exitInternal
	}
	configPath := findArgValue(os.Args[1:], "--config")
	cfg := config.Default()
	if configPath != "" {
		cfg, err = config.Load(configPath, cfg)
	} else {
		cfg, _, err = config.FindAndLoad(cwd)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "configuration error:", err)
		return exitInput
	}

	fs := flag.NewFlagSet("mutation-judge", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	operators := fs.String("operators", strings.Join(cfg.Operators, ","), "comma-separated operators: boundary,boolean,arithmetic,errorreturn,switch,loop,channel")
	timeout := fs.Duration("timeout", cfg.Timeout, "maximum time for each test run")
	testRun := fs.String("test-run", cfg.TestRun, "Go test -run regular expression")
	format := fs.String("format", cfg.Format, "report format: text, json, html")
	output := fs.String("output", cfg.Output, "write report to this path instead of stdout")
	cacheDir := fs.String("cache-dir", cfg.CacheDir, "content-addressed cache directory")
	noCache := fs.Bool("no-cache", !cfg.Cache, "disable the result cache")
	maxMutants := fs.Int("max-mutants", cfg.MaxMutants, "maximum mutants to execute; zero means unlimited")
	changed := fs.String("changed", cfg.ChangedBase, "mutate only lines changed from this Git revision")
	ciMin := fs.Float64("ci-min-score", cfg.CIMinScore, "fail policy when score is below this percentage; zero disables")
	ciCode := fs.Int("ci-exit-code", cfg.CIExitCode, "exit code used for CI policy failure")
	includeGenerated := fs.Bool("include-generated", cfg.IncludeGenerated, "include generated Go source")
	progress := fs.Bool("progress", cfg.Progress, "emit one progress line per mutant to stderr")
	narrowTestScope := fs.Bool("narrow-test-scope", cfg.NarrowTestScope, "run each mutant only against tests that can observe it, computed from the module's own dependency graph, instead of the full pattern set every time; opt-in, see docs/performance.md")
	workers := fs.Int("workers", cfg.Workers, "run this many mutants concurrently, each in its own sandbox; 0 or 1 (the default) runs sequentially, unchanged from earlier versions; see docs/performance.md")
	printConfig := fs.Bool("print-config", false, "print the effective configuration and exit")
	showVersion := fs.Bool("version", false, "print version and exit")
	_ = fs.String("config", configPath, "configuration file (TOML or YAML)")
	fs.Usage = func() {
		fmt.Fprintln(fs.Output(), "Usage: mutation-judge [flags] [package patterns]")
		fmt.Fprintln(fs.Output(), "Examples:\n  mutation-judge ./...\n  mutation-judge --changed origin/main ./...\n  mutation-judge --operators boundary,boolean ./pkg/...")
		fs.PrintDefaults()
	}
	if err := fs.Parse(os.Args[1:]); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return exitOK
		}
		return exitInput
	}
	if *showVersion {
		fmt.Println(version)
		return exitOK
	}
	cfg.Operators = splitComma(*operators)
	cfg.Timeout = *timeout
	cfg.TestRun = *testRun
	cfg.Format = *format
	cfg.Output = *output
	cfg.CacheDir = *cacheDir
	cfg.Cache = !*noCache
	cfg.MaxMutants = *maxMutants
	cfg.ChangedBase = *changed
	cfg.CIMinScore = *ciMin
	cfg.CIExitCode = *ciCode
	cfg.IncludeGenerated = *includeGenerated
	cfg.Progress = *progress
	cfg.NarrowTestScope = *narrowTestScope
	cfg.Workers = *workers
	if err := config.Validate(cfg); err != nil {
		fmt.Fprintln(os.Stderr, "configuration error:", err)
		return exitInput
	}
	if *printConfig {
		b, err := json.MarshalIndent(cfg.AsMap(), "", "  ")
		if err != nil {
			fmt.Fprintln(os.Stderr, "configuration error:", err)
			return exitInternal
		}
		fmt.Println(string(b))
		return exitOK
	}
	patterns := fs.Args()
	if len(patterns) == 0 {
		patterns = []string{"./..."}
	}

	engine := analysis.Engine{Version: version}
	if cfg.Progress {
		engine.Progress = func(p analysis.Progress) {
			fmt.Fprintf(os.Stderr, "[%d/%d] %s %s %s:%d\n", p.Index, p.Total, p.Mutation.ID, p.Mutation.Operator, p.Mutation.Span.File, p.Mutation.Span.StartLine)
		}
	}
	// Cancelling on SIGINT/SIGTERM (rather than leaving context.Background
	// with no signal handler at all) is what actually lets the deferred
	// sandbox cleanup in workspace/analysis run: Go's default disposition
	// for an unhandled SIGINT/SIGTERM is immediate process termination,
	// which never unwinds deferred functions. exec.CommandContext also
	// kills any in-flight `go test` child the moment this context is done.
	//
	// This is written out explicitly (rather than using the equivalent
	// signal.NotifyContext helper) so the specific signal that fired can
	// be recorded in the interruption journal below -- SIGTERM usually
	// means an orchestrator or CI timeout killed the process, SIGINT
	// usually means a person hit Ctrl+C, and that distinction is useful
	// for post-mortem debugging. caught is buffered so the send in the
	// goroutine below can never block, and it happens strictly before the
	// cancel() call that unblocks anything waiting on ctx.Done(), so a
	// non-blocking receive from caught after observing ctx.Err() != nil
	// is race-free: the value is guaranteed to already be there.
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, os.Interrupt, syscall.SIGTERM)
	ctx, cancel := context.WithCancel(context.Background())
	caught := make(chan os.Signal, 1)
	go func() {
		select {
		case sig := <-sigCh:
			caught <- sig
			cancel()
		case <-ctx.Done():
		}
	}()
	defer func() {
		signal.Stop(sigCh)
		cancel()
	}()
	r, err := engine.Analyze(ctx, analysis.Request{CWD: cwd, Patterns: patterns, Config: cfg})
	interrupted := ctx.Err() != nil
	var caughtSignal os.Signal
	if interrupted {
		select {
		case caughtSignal = <-caught:
		default:
		}
	}
	if err != nil {
		if interrupted {
			fmt.Fprintln(os.Stderr, "mutation-judge: interrupted")
			if jerr := appendJournalEntry(cwd, journalEntry{
				Time: time.Now().UTC(), Signal: signalName(caughtSignal), Phase: "baseline",
				ToolVersion: version, Patterns: patterns, Operators: cfg.Operators, ExitCode: exitInterrupted,
			}); jerr != nil {
				fmt.Fprintln(os.Stderr, "warning: could not write interruption journal:", jerr)
			}
			return exitInterrupted
		}
		fmt.Fprintln(os.Stderr, "analysis error:", err)
		return exitInput
	}
	for _, w := range r.Warnings {
		fmt.Fprintln(os.Stderr, "warning:", w)
	}
	dst := os.Stdout
	var f *os.File
	if cfg.Output != "" {
		if err := os.MkdirAll(parentDir(cfg.Output), 0o755); err != nil {
			fmt.Fprintln(os.Stderr, err)
			return exitInput
		}
		f, err = os.Create(cfg.Output)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return exitInput
		}
		dst = f
	}
	if err := report.RenderMeasured(dst, cfg.Format, &r); err != nil {
		if f != nil {
			_ = f.Close()
		}
		fmt.Fprintln(os.Stderr, "report error:", err)
		return exitInternal
	}
	if f != nil {
		if err := f.Close(); err != nil {
			fmt.Fprintln(os.Stderr, "report error:", err)
			return exitInternal
		}
	}
	if interrupted {
		// The report (possibly partial, with complete=false) has already
		// been rendered above so the work done before the signal isn't
		// lost. The exit code still needs to clearly say "interrupted"
		// rather than being folded into the CI score policy below, so a
		// script can tell "mutation score too low" apart from "the user
		// hit Ctrl+C".
		if jerr := appendJournalEntry(cwd, journalEntry{
			Time: time.Now().UTC(), Signal: signalName(caughtSignal), Phase: "mutants",
			ToolVersion: version, Patterns: patterns, Operators: cfg.Operators, ExitCode: exitInterrupted,
			CompletedMutants: len(r.Results), RetainedMutants: retainedMutants(r),
		}); jerr != nil {
			fmt.Fprintln(os.Stderr, "warning: could not write interruption journal:", jerr)
		}
		return exitInterrupted
	}
	if cfg.CIMinScore > 0 && r.Summary.Killed+r.Summary.Survived > 0 && r.Summary.Score < cfg.CIMinScore {
		return cfg.CIExitCode
	}
	return exitOK
}

func splitComma(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

func findArgValue(args []string, name string) string {
	for i, a := range args {
		if a == name && i+1 < len(args) {
			return args[i+1]
		}
		if strings.HasPrefix(a, name+"=") {
			return strings.TrimPrefix(a, name+"=")
		}
	}
	return ""
}

func parentDir(path string) string {
	d := filepath.Dir(path)
	if d == "." {
		return "./"
	}
	return d
}

// signalName returns a stable, lowercase name for the journal ("interrupt",
// "terminated"), or "unknown" if a journal entry is written without a
// captured signal (defensive; should not happen given how caughtSignal is
// populated above).
func signalName(sig os.Signal) string {
	if sig == nil {
		return "unknown"
	}
	return sig.String()
}

// retainedMutants reads the "retained_after_bound" bound recorded on a
// partial report, if present, for the journal entry. It is best-effort:
// a missing or wrong-typed value just means the journal omits the field.
func retainedMutants(r model.Report) int {
	if v, ok := r.Bounds["retained_after_bound"].(int); ok {
		return v
	}
	return 0
}
