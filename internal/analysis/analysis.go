package analysis

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/example/mutation-judge/internal/cache"
	"github.com/example/mutation-judge/internal/config"
	covermap "github.com/example/mutation-judge/internal/coverage"
	"github.com/example/mutation-judge/internal/frontend"
	"github.com/example/mutation-judge/internal/gitdiff"
	"github.com/example/mutation-judge/internal/model"
	"github.com/example/mutation-judge/internal/runner"
	"github.com/example/mutation-judge/internal/workspace"
)

type Progress struct {
	Index    int
	Total    int
	Mutation model.Mutation
}

type Engine struct {
	Version  string
	Backend  runner.Backend
	Progress func(Progress)
}

type Request struct {
	CWD      string
	Patterns []string
	Config   config.Config
}

type preparedAnalysis struct {
	root           string
	workRel        string
	sandbox        string
	cleanup        func()
	mutants        []model.Mutation
	discovered     int
	parsingMS      int64
	sourceDigest   string
	coveragePath   string
	backendName    string
	backendVersion string
}

func (e Engine) Analyze(ctx context.Context, req Request) (model.Report, error) {
	started := time.Now()
	if e.Backend == nil {
		e.Backend = runner.GoTest{}
	}

	prepared, err := e.prepare(req)
	if err != nil {
		return model.Report{}, err
	}
	defer prepared.cleanup()

	coverage, coverageKnown, baselineMS, err := e.runBaseline(ctx, req, prepared)
	if err != nil {
		return model.Report{}, err
	}

	results, complete, executionMS, warnings, err := e.executeMutants(ctx, req, prepared, coverage, coverageKnown)
	if err != nil {
		return model.Report{}, err
	}
	return buildReport(e.Version, req, prepared, results, complete, baselineMS, executionMS, warnings, started), nil
}

func (e Engine) prepare(req Request) (preparedAnalysis, error) {
	root, err := workspace.ModuleRoot(req.CWD)
	if err != nil {
		return preparedAnalysis{}, err
	}
	workRel, err := filepath.Rel(root, req.CWD)
	if err != nil || workRel == ".." || strings.HasPrefix(workRel, ".."+string(filepath.Separator)) {
		return preparedAnalysis{}, fmt.Errorf("working directory must be inside module root %s", root)
	}
	pkgs, err := workspace.ListPackages(req.CWD, req.Patterns)
	if err != nil {
		return preparedAnalysis{}, err
	}
	files, err := workspace.SourceFiles(root, pkgs)
	if err != nil {
		return preparedAnalysis{}, err
	}

	parseStart := time.Now()
	var changed map[string]map[int]bool
	if req.Config.ChangedBase != "" {
		changed, err = gitdiff.ChangedLines(root, req.Config.ChangedBase)
		if err != nil {
			return preparedAnalysis{}, err
		}
	}
	opset := make(map[string]bool, len(req.Config.Operators))
	for _, op := range req.Config.Operators {
		opset[op] = true
	}
	mutants, err := frontend.Discover(root, files, frontend.Options{
		Operators:        opset,
		IncludeGenerated: req.Config.IncludeGenerated,
		ChangedLines:     changed,
	})
	if err != nil {
		return preparedAnalysis{}, err
	}
	discovered := len(mutants)
	if req.Config.MaxMutants > 0 && len(mutants) > req.Config.MaxMutants {
		mutants = mutants[:req.Config.MaxMutants]
	}
	parsingMS := time.Since(parseStart).Milliseconds()

	sourceDigest, err := workspace.Digest(root)
	if err != nil {
		return preparedAnalysis{}, err
	}
	sandbox, cleanup, err := workspace.CopyModule(root, req.Config.CacheDir)
	if err != nil {
		return preparedAnalysis{}, err
	}
	coveragePath := filepath.Join(sandbox, ".mutation-judge", "coverage.out")
	if err := os.MkdirAll(filepath.Dir(coveragePath), 0o755); err != nil {
		cleanup()
		return preparedAnalysis{}, err
	}
	backendName, backendVersion := backendIdentity(e.Backend)
	return preparedAnalysis{
		root: root, workRel: filepath.ToSlash(workRel), sandbox: sandbox, cleanup: cleanup,
		mutants: mutants, discovered: discovered, parsingMS: parsingMS, sourceDigest: sourceDigest,
		coveragePath: coveragePath, backendName: backendName, backendVersion: backendVersion,
	}, nil
}

func (e Engine) runBaseline(ctx context.Context, req Request, p preparedAnalysis) (covermap.Map, bool, int64, error) {
	started := time.Now()
	baseline := e.Backend.Run(ctx, runner.Request{
		Root: p.sandbox, WorkRel: p.workRel, Patterns: req.Patterns,
		TestRun: req.Config.TestRun, Timeout: req.Config.Timeout, CoverageOut: p.coveragePath,
	})
	elapsed := time.Since(started).Milliseconds()
	if baseline.Verdict != model.VerdictSurvived {
		return covermap.Map{}, false, elapsed, fmt.Errorf("baseline tests must pass before mutation analysis (verdict %s):\n%s", baseline.Verdict, baseline.Output)
	}
	coverage, err := covermap.Parse(p.coveragePath, p.sandbox)
	return coverage, err == nil, elapsed, nil
}

func (e Engine) executeMutants(ctx context.Context, req Request, p preparedAnalysis, coverage covermap.Map, coverageKnown bool) ([]model.Result, bool, int64, []string, error) {
	cfgJSON, err := json.Marshal(req.Config.AsMap())
	if err != nil {
		return nil, false, 0, nil, fmt.Errorf("marshal effective configuration for cache key: %w", err)
	}
	cacheDir := req.Config.CacheDir
	if !filepath.IsAbs(cacheDir) {
		cacheDir = filepath.Join(p.root, cacheDir)
	}
	store := cache.Store{Dir: cacheDir, Enabled: req.Config.Cache}
	results := make([]model.Result, 0, len(p.mutants))
	complete := true
	started := time.Now()
	executedCount := 0
	cacheWriteFailures := 0
	var firstCacheErr error

	for i, mut := range p.mutants {
		if ctx.Err() != nil {
			complete = false
			break
		}
		if e.Progress != nil {
			e.Progress(Progress{Index: i + 1, Total: len(p.mutants), Mutation: mut})
		}
		covered, known := false, false
		if coverageKnown {
			covered, known = coverage.Covered(mut.Span.File, mut.Span.StartLine, mut.Span.EndLine)
		}
		key := cache.Key(
			e.Version,
			frontend.SemanticsVersion,
			runtime.Version(),
			p.sourceDigest,
			string(cfgJSON),
			p.backendName,
			p.backendVersion,
			mut.ID,
			mut.Replacement,
		)
		backendResult, hit := store.Get(key)
		if !hit {
			restore, err := workspace.Apply(p.sandbox, mut.Span.File, mut.Span.StartByte, mut.Span.EndByte, mut.Replacement)
			if err != nil {
				return nil, false, 0, nil, err
			}
			backendResult = e.Backend.Run(ctx, runner.Request{
				Root: p.sandbox, WorkRel: p.workRel, Patterns: req.Patterns,
				TestRun: req.Config.TestRun, Timeout: req.Config.Timeout,
			})
			if restoreErr := restore(); restoreErr != nil {
				return nil, false, 0, nil, fmt.Errorf("restore %s after %s: %w", mut.Span.File, mut.ID, restoreErr)
			}
			executedCount++
			// A cache write failure does not invalidate this mutant's
			// result -- it was still correctly executed and classified --
			// so it stays non-fatal. But silently discarding the error
			// left the person with no way to know caching stopped
			// working (e.g. a full or permission-denied cache_dir), so
			// it's now surfaced as an evidence field on the report (see
			// model.Report.Warnings) plus a stderr line, deduplicated
			// into one summary rather than one line per mutant.
			if putErr := store.Put(key, backendResult); putErr != nil {
				cacheWriteFailures++
				if firstCacheErr == nil {
					firstCacheErr = putErr
				}
			}
		}
		results = append(results, makeResult(mut, backendResult, hit, covered, known))
		if ctx.Err() != nil {
			complete = false
			break
		}
	}
	var warnings []string
	if cacheWriteFailures > 0 {
		warnings = append(warnings, fmt.Sprintf(
			"cache write failed for %d of %d freshly executed mutant(s); those results are correct but were not cached for reuse (first error: %v)",
			cacheWriteFailures, executedCount, firstCacheErr,
		))
	}
	return results, complete, time.Since(started).Milliseconds(), warnings, nil
}

func buildReport(toolVersion string, req Request, p preparedAnalysis, results []model.Result, complete bool, baselineMS, executionMS int64, warnings []string, started time.Time) model.Report {
	return model.Report{
		SchemaVersion:   model.SchemaVersion,
		ToolVersion:     toolVersion,
		GoVersion:       runtime.Version(),
		GeneratedAt:     time.Now().UTC(),
		Complete:        complete,
		Patterns:        append([]string(nil), req.Patterns...),
		EffectiveConfig: req.Config.AsMap(),
		Bounds: map[string]any{
			"max_mutants":             req.Config.MaxMutants,
			"per_mutant_timeout":      req.Config.Timeout.String(),
			"discovered_before_bound": p.discovered,
			"retained_after_bound":    len(p.mutants),
			"completed_mutants":       len(results),
			"backend_name":            p.backendName,
			"backend_version":         p.backendVersion,
			"operator_semantics":      frontend.SemanticsVersion,
			"test_command":            testCommand(req.Config, req.Patterns),
		},
		Semantics: []string{
			"Each mutant is a single source-span replacement applied atomically to a temporary copy of the Go module.",
			"A passing selected go test command means SURVIVED; a failing test, including a runtime panic, means KILLED; compilation/type errors mean INVALID; deadline expiry means TIMEOUT.",
			"INVALID, TIMEOUT, UNKNOWN, and UNSUPPORTED mutants are excluded from the mutation-score denominator.",
			"Coverage is baseline statement coverage from the selected tests and is explanatory, not a substitute for executing a mutant.",
		},
		Warnings: warnings,
		Summary:  summarize(results),
		Results:  results,
		Timing: model.Timing{
			ParsingMS: p.parsingMS, BaselineMS: baselineMS, ExecutionMS: executionMS,
			TotalMS: time.Since(started).Milliseconds(),
		},
	}
}

func makeResult(mut model.Mutation, rr runner.Result, cached, covered, coverageKnown bool) model.Result {
	statement := strings.ToLower(string(rr.Verdict)) + ": " + mut.Description
	evidence := map[string]any{"source_diff": mut.Diff, "backend_exit_code": rr.ExitCode}
	if len(rr.Tests) > 0 {
		evidence["responsible_tests"] = rr.Tests
	}
	if rr.Output != "" && rr.Verdict != model.VerdictSurvived {
		evidence["backend_output"] = rr.Output
	}
	if coverageKnown {
		evidence["baseline_covered"] = covered
	}
	suggestion := ""
	if rr.Verdict == model.VerdictSurvived {
		suggestion = mut.Suggestion
	}
	return model.Result{
		Mutation: mut, Verdict: rr.Verdict, Responsible: rr.Tests, Covered: covered, CoverageKnown: coverageKnown,
		Cached: cached, DurationMS: rr.DurationMS, Output: rr.Output,
		Diagnostic: model.Diagnostic{
			ID: verdictRule(rr.Verdict), Location: mut.Span, Statement: statement, Evidence: evidence,
			Assumptions: []string{"selected tests are represented by the reported go test command", "one mutant is active at a time"},
			Suggestion:  suggestion,
		},
	}
}

func verdictRule(v model.Verdict) string {
	switch v {
	case model.VerdictKilled:
		return "MJ-VERDICT-KILLED"
	case model.VerdictSurvived:
		return "MJ-VERDICT-SURVIVED"
	case model.VerdictInvalid:
		return "MJ-VERDICT-INVALID"
	case model.VerdictTimeout:
		return "MJ-VERDICT-TIMEOUT"
	case model.VerdictUnsupported:
		return "MJ-VERDICT-UNSUPPORTED"
	default:
		return "MJ-VERDICT-UNKNOWN"
	}
}

func summarize(results []model.Result) model.Summary {
	s := model.Summary{Generated: len(results)}
	for _, r := range results {
		switch r.Verdict {
		case model.VerdictKilled:
			s.Killed++
		case model.VerdictSurvived:
			s.Survived++
		case model.VerdictInvalid:
			s.Invalid++
		case model.VerdictTimeout:
			s.Timeout++
		case model.VerdictUnsupported:
			s.Unsupported++
		default:
			s.Unknown++
		}
	}
	den := s.Killed + s.Survived
	if den == 0 {
		s.Score = 0
		s.ScoreText = "n/a (no scoreable mutants)"
	} else {
		s.Score = 100 * float64(s.Killed) / float64(den)
		s.ScoreText = fmt.Sprintf("%.1f%% excluding invalid/timeout/unknown/unsupported", s.Score)
	}
	return s
}

func testCommand(cfg config.Config, patterns []string) []string {
	cmd := []string{"go", "test", "-count=1", "-timeout", cfg.Timeout.String()}
	if cfg.TestRun != "" {
		cmd = append(cmd, "-run", cfg.TestRun)
	}
	return append(cmd, patterns...)
}

func backendIdentity(b runner.Backend) (string, string) {
	if described, ok := b.(runner.DescribedBackend); ok {
		return described.Name(), described.Version()
	}
	return fmt.Sprintf("%T", b), "unspecified"
}
