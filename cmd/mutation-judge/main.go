package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"runtime/debug"
	"strings"

	"github.com/example/mutation-judge/internal/analysis"
	"github.com/example/mutation-judge/internal/config"
	"github.com/example/mutation-judge/internal/report"
)

const version = "0.1.3"
const (
	exitOK       = 0
	exitInput    = 2
	exitInternal = 3
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
	operators := fs.String("operators", strings.Join(cfg.Operators, ","), "comma-separated operators: boundary,boolean,arithmetic")
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
	r, err := engine.Analyze(context.Background(), analysis.Request{CWD: cwd, Patterns: patterns, Config: cfg})
	if err != nil {
		fmt.Fprintln(os.Stderr, "analysis error:", err)
		return exitInput
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
