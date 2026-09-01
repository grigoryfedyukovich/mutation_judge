package analysis

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
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
	toolchain      runner.ToolchainInfo
	filePackage    map[string]string   // relative file path -> owning package import path
	testScopes     map[string][]string // package import path -> minimal safe go test patterns; nil unless NarrowTestScope is on
}

func (e Engine) Analyze(ctx context.Context, req Request) (model.Report, error) {
	started := time.Now()
	if e.Backend == nil {
		e.Backend = runner.GoTest{}
	}

	// Detected once, up front: every mutant execution needs a working
	// `go` on PATH regardless of backend, and the invoked toolchain
	// (not runtime.Version(), the toolchain that happened to compile
	// this mutation-judge binary) is what actually determines a
	// mutant's compiled/runtime behavior -- so it belongs in the cache
	// key and the report, and failing fast here beats discovering a
	// missing/broken `go` only after paying for sandbox setup and
	// discovery first.
	toolchain, err := runner.DetectToolchain(ctx)
	if err != nil {
		return model.Report{}, err
	}

	prepared, err := e.prepare(req, toolchain)
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

// mutantTestScope returns the go test patterns to use for one mutant's
// execution. It is req.Patterns unchanged unless config.NarrowTestScope
// is enabled and a scope was successfully computed for the mutant's own
// package (see workspace.TestScopes) -- in which case that narrower,
// safe scope is used instead. Any uncertainty at all (the mutant's file
// couldn't be mapped to a package, or no scope was recorded for that
// package) falls back to the full req.Patterns rather than risking
// running too little; narrowing is only ever a speed optimization, never
// something this function is willing to guess about.
func mutantTestScope(req Request, p preparedAnalysis, mut model.Mutation) []string {
	if !req.Config.NarrowTestScope {
		return req.Patterns
	}
	pkg, ok := p.filePackage[mut.Span.File]
	if !ok {
		return req.Patterns
	}
	scope, ok := p.testScopes[pkg]
	if !ok || len(scope) == 0 {
		return req.Patterns
	}
	return scope
}

func (e Engine) prepare(req Request, toolchain runner.ToolchainInfo) (preparedAnalysis, error) {
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
	filePackage, err := fileOwningPackages(root, pkgs)
	if err != nil {
		return preparedAnalysis{}, err
	}
	var testScopes map[string][]string
	if req.Config.NarrowTestScope {
		testScopes, err = workspace.TestScopes(req.CWD, req.Patterns)
		if err != nil {
			return preparedAnalysis{}, err
		}
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

	sourceDigest, err := workspace.Digest(root, req.Config.CacheDir)
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
		coveragePath: coveragePath, backendName: backendName, backendVersion: backendVersion, toolchain: toolchain,
		filePackage: filePackage, testScopes: testScopes,
	}, nil
}

// fileOwningPackages maps each source file SourceFiles would return
// (relative to root, forward-slashed) to the import path of the package
// it belongs to, for looking up a mutant's minimal safe test scope (see
// workspace.TestScopes) by the file its span is in. Errors the same way
// SourceFiles does on a package-loading failure, since both walk the
// same pkgs slice.
func fileOwningPackages(root string, pkgs []workspace.Package) (map[string]string, error) {
	out := map[string]string{}
	for _, p := range pkgs {
		if p.Error != nil && p.Error.Err != "" {
			return nil, fmt.Errorf("package %s: %s", p.ImportPath, p.Error.Err)
		}
		for _, name := range append(append([]string{}, p.GoFiles...), p.CgoFiles...) {
			abs := filepath.Join(p.Dir, name)
			rel, err := filepath.Rel(root, abs)
			if err != nil {
				return nil, err
			}
			out[filepath.ToSlash(rel)] = p.ImportPath
		}
	}
	return out, nil
}

func (e Engine) runBaseline(ctx context.Context, req Request, p preparedAnalysis) (covermap.Map, bool, int64, error) {
	started := time.Now()
	baseline := e.Backend.Run(ctx, runner.Request{
		Root: p.sandbox, WorkRel: p.workRel, Patterns: req.Patterns,
		TestRun: req.Config.TestRun, Timeout: req.Config.Timeout, CoverageOut: p.coveragePath,
		GoVersion: p.toolchain.GoVersion,
	})
	elapsed := time.Since(started).Milliseconds()
	if baseline.Verdict != model.VerdictSurvived {
		return covermap.Map{}, false, elapsed, fmt.Errorf("baseline tests must pass before mutation analysis (verdict %s):\n%s", baseline.Verdict, baseline.Output)
	}
	coverage, err := covermap.Parse(p.coveragePath, p.sandbox)
	return coverage, err == nil, elapsed, nil
}

// runOneMutant executes a single mutant against a given sandbox and is
// shared, unmodified, by both the sequential and parallel execution
// paths below -- extracted specifically so there is one place that
// decides cache keys, applies/restores the mutation, and classifies the
// result, rather than two independently-maintained copies that could
// drift out of sync with each other over time.
// cacheRelevantConfig is the subset of configuration whose value could
// actually affect an individual mutant's own test outcome, used for the
// cache key instead of the full Config.AsMap(). Fields that only affect
// orchestration, reporting, or concurrency -- how many workers run
// mutants, what format the report renders in, whether progress lines
// print, the CI failure policy, which operators discover mutants in the
// first place -- must not be part of this, or changing any of them
// would wastefully invalidate every cached result despite the actual
// `go test` command for a given, already-identified mutant being
// completely unaffected. Exactly Timeout and TestRun are what actually
// reach runner.Request in runOneMutant below; nothing else from Config
// does (Patterns comes from the computed test scope, not directly from
// Config, and is already captured separately in the cache key via that
// scope string).
//
// This exists because of a real regression, found by testing the
// finished --workers feature rather than assuming it worked: adding a
// "workers" key to Config.AsMap() for --print-config visibility also
// silently widened the cache key (which had been using the full
// AsMap()) to be sensitive to worker count, so running the same
// analysis with a different --workers value produced zero cache hits
// despite every mutant's actual test outcome being identical.
func cacheRelevantConfig(c config.Config) map[string]any {
	return map[string]any{
		"timeout":  c.Timeout.String(),
		"test_run": c.TestRun,
	}
}

func (e Engine) runOneMutant(ctx context.Context, req Request, p preparedAnalysis, store cache.Store, cfgJSON []byte, sandbox string, mut model.Mutation, coverage covermap.Map, coverageKnown bool) (result model.Result, executed bool, cachePutErr error, hardErr error) {
	covered, known := false, false
	if coverageKnown {
		covered, known = coverage.Covered(mut.Span.File, mut.Span.StartLine, mut.Span.EndLine)
	}
	// A mutant discovery itself already proved equivalent (see
	// Mutation.EquivalentReason and frontend.detectGuardedComparison)
	// never runs at all: there is no test outcome to observe that
	// could change the answer, and running it anyway would just spend
	// a `go test` invocation to relearn something already proven. This
	// intentionally bypasses the cache too -- there is nothing to
	// cache a lookup key for, since no backend ever ran.
	if mut.EquivalentReason != "" {
		return makeEquivalentResult(mut, covered, known), false, nil, nil
	}
	scope := mutantTestScope(req, p, mut)
	key := cache.Key(
		e.Version,
		frontend.SemanticsVersion,
		p.toolchain.Key(),
		p.sourceDigest,
		string(cfgJSON),
		p.backendName,
		p.backendVersion,
		mut.ID,
		mut.Replacement,
		strings.Join(scope, ","),
	)
	backendResult, hit := store.Get(key)
	if !hit {
		restore, err := workspace.Apply(sandbox, mut.Span.File, mut.Span.StartByte, mut.Span.EndByte, mut.Replacement)
		if err != nil {
			return model.Result{}, false, nil, err
		}
		backendResult = e.Backend.Run(ctx, runner.Request{
			Root: sandbox, WorkRel: p.workRel, Patterns: scope,
			TestRun: req.Config.TestRun, Timeout: req.Config.Timeout,
			GoVersion: p.toolchain.GoVersion,
		})
		if restoreErr := restore(); restoreErr != nil {
			return model.Result{}, false, nil, fmt.Errorf("restore %s after %s: %w", mut.Span.File, mut.ID, restoreErr)
		}
		executed = true
		// A cache write failure does not invalidate this mutant's
		// result -- it was still correctly executed and classified --
		// so it stays non-fatal; the caller aggregates these into the
		// report's Warnings evidence field instead of failing outright.
		if putErr := store.Put(key, backendResult); putErr != nil {
			cachePutErr = putErr
		}
	}
	return makeResult(mut, backendResult, hit, covered, known), executed, cachePutErr, nil
}

func (e Engine) executeMutants(ctx context.Context, req Request, p preparedAnalysis, coverage covermap.Map, coverageKnown bool) ([]model.Result, bool, int64, []string, error) {
	if req.Config.Workers > 1 {
		return e.executeMutantsParallel(ctx, req, p, coverage, coverageKnown)
	}
	return e.executeMutantsSequential(ctx, req, p, coverage, coverageKnown)
}

func (e Engine) executeMutantsSequential(ctx context.Context, req Request, p preparedAnalysis, coverage covermap.Map, coverageKnown bool) ([]model.Result, bool, int64, []string, error) {
	cfgJSON, err := json.Marshal(cacheRelevantConfig(req.Config))
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
		result, executed, cachePutErr, hardErr := e.runOneMutant(ctx, req, p, store, cfgJSON, p.sandbox, mut, coverage, coverageKnown)
		if hardErr != nil {
			return nil, false, 0, nil, hardErr
		}
		if executed {
			executedCount++
			if cachePutErr != nil {
				cacheWriteFailures++
				if firstCacheErr == nil {
					firstCacheErr = cachePutErr
				}
			}
		}
		results = append(results, result)
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

// executeMutantsParallel is the opt-in (config.Workers > 1) counterpart
// to executeMutantsSequential above, sharing the exact same per-mutant
// logic via runOneMutant. Each worker gets its own fully independent
// sandbox (created the same way the single sequential sandbox is,
// cheaply where the platform supports copy-on-write cloning -- see
// docs/performance.md), which is what makes concurrent execution safe:
// nothing in the workspace/runner/cache packages holds shared mutable
// state, so two workers applying/running/restoring mutations on two
// DIFFERENT sandbox directories can never conflict with each other. That
// was verified by review before this was written, not assumed -- see
// docs/performance.md for the specific things checked.
//
// Output ordering is deterministic despite parallel, out-of-order
// completion: results are written into a slice pre-sized and indexed by
// each mutant's position in the (already deterministically ordered)
// discovery order, then collected back into a plain slice in that same
// order at the end -- never in whichever order workers happened to
// finish. Running the same analysis twice with the same worker count
// produces results in the same order every time (see
// TestParallelExecutionIsDeterministic).
//
// A hard error from any one worker (an Apply or restore failure, as
// opposed to an ordinary mutant verdict) cancels an inner context
// derived from ctx, which every worker and the work-dispatching
// goroutine observe on their next iteration -- reusing the same
// cancellation mechanism that already stops in-flight `go test`
// processes on SIGINT/SIGTERM, rather than inventing a second one.
func (e Engine) executeMutantsParallel(ctx context.Context, req Request, p preparedAnalysis, coverage covermap.Map, coverageKnown bool) ([]model.Result, bool, int64, []string, error) {
	started := time.Now()
	if len(p.mutants) == 0 {
		return nil, true, 0, nil, nil
	}
	cfgJSON, err := json.Marshal(cacheRelevantConfig(req.Config))
	if err != nil {
		return nil, false, 0, nil, fmt.Errorf("marshal effective configuration for cache key: %w", err)
	}
	cacheDir := req.Config.CacheDir
	if !filepath.IsAbs(cacheDir) {
		cacheDir = filepath.Join(p.root, cacheDir)
	}
	store := cache.Store{Dir: cacheDir, Enabled: req.Config.Cache}

	workers := req.Config.Workers
	if workers > len(p.mutants) {
		workers = len(p.mutants) // no point creating more sandboxes than there are mutants to run
	}

	sandboxes := make([]string, 0, workers)
	var sandboxCleanups []func()
	defer func() {
		for _, c := range sandboxCleanups {
			c()
		}
	}()
	for i := 0; i < workers; i++ {
		sandbox, cleanup, err := workspace.CopyModule(p.root, req.Config.CacheDir)
		if err != nil {
			return nil, false, 0, nil, fmt.Errorf("create sandbox for worker %d: %w", i, err)
		}
		sandboxes = append(sandboxes, sandbox)
		sandboxCleanups = append(sandboxCleanups, cleanup)
	}

	innerCtx, cancelInner := context.WithCancel(ctx)
	defer cancelInner()

	results := make([]*model.Result, len(p.mutants))
	var mu sync.Mutex // guards results, hardErr, executedCount, cacheWriteFailures, firstCacheErr
	var hardErr error
	executedCount := 0
	cacheWriteFailures := 0
	var firstCacheErr error
	var progressMu sync.Mutex // guards e.Progress calls only, kept separate from mu so a slow progress callback never blocks result bookkeeping

	work := make(chan int)
	var wg sync.WaitGroup
	for _, sandbox := range sandboxes {
		wg.Add(1)
		go func(sandbox string) {
			defer wg.Done()
			for idx := range work {
				if innerCtx.Err() != nil {
					continue // drain the rest of the channel without processing; the dispatcher below has also already stopped sending
				}
				mut := p.mutants[idx]
				if e.Progress != nil {
					progressMu.Lock()
					e.Progress(Progress{Index: idx + 1, Total: len(p.mutants), Mutation: mut})
					progressMu.Unlock()
				}
				result, executed, cachePutErr, err := e.runOneMutant(innerCtx, req, p, store, cfgJSON, sandbox, mut, coverage, coverageKnown)
				if err != nil {
					mu.Lock()
					if hardErr == nil {
						hardErr = err
					}
					mu.Unlock()
					cancelInner()
					continue
				}
				mu.Lock()
				results[idx] = &result
				if executed {
					executedCount++
					if cachePutErr != nil {
						cacheWriteFailures++
						if firstCacheErr == nil {
							firstCacheErr = cachePutErr
						}
					}
				}
				mu.Unlock()
			}
		}(sandbox)
	}

	go func() {
		defer close(work)
		for i := range p.mutants {
			select {
			case work <- i:
			case <-innerCtx.Done():
				return
			}
		}
	}()

	wg.Wait()

	if hardErr != nil {
		return nil, false, 0, nil, hardErr
	}

	final := make([]model.Result, 0, len(p.mutants))
	complete := true
	for _, r := range results {
		if r == nil {
			complete = false
			continue
		}
		final = append(final, *r)
	}

	var warnings []string
	if cacheWriteFailures > 0 {
		warnings = append(warnings, fmt.Sprintf(
			"cache write failed for %d of %d freshly executed mutant(s); those results are correct but were not cached for reuse (first error: %v)",
			cacheWriteFailures, executedCount, firstCacheErr,
		))
	}
	return final, complete, time.Since(started).Milliseconds(), warnings, nil
}

func buildReport(toolVersion string, req Request, p preparedAnalysis, results []model.Result, complete bool, baselineMS, executionMS int64, warnings []string, started time.Time) model.Report {
	return model.Report{
		SchemaVersion:   model.SchemaVersion,
		ToolVersion:     toolVersion,
		GoVersion:       p.toolchain.GoVersion,
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
			"goos":                    p.toolchain.GOOS,
			"goarch":                  p.toolchain.GOARCH,
			"cgo_enabled":             p.toolchain.CgoEnabled,
			"goflags":                 p.toolchain.GoFlags,
		},
		Semantics: []string{
			"Each mutant is a single source-span replacement applied atomically to a temporary copy of the Go module.",
			"A passing selected go test command means SURVIVED; a failing test, including a runtime panic, means KILLED; compilation/type errors mean INVALID; deadline expiry means TIMEOUT.",
			"EQUIVALENT means discovery proved the mutant behaviorally identical to the original before any test ran (see each such result's diagnostic for the specific proof); it is never executed.",
			"INVALID, TIMEOUT, UNKNOWN, UNSUPPORTED, and EQUIVALENT mutants are excluded from the mutation-score denominator.",
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

// makeEquivalentResult builds the Result for a mutant discovery
// already proved equivalent, without ever invoking the backend. Its
// Diagnostic carries the actual proof (mut.EquivalentReason) as the
// statement, not a generic "equivalent" label, so a report reader (or
// --format sarif/github, which excludes this verdict from findings
// entirely -- see report.sarifIncluded/githubLevel) can see exactly
// why without re-deriving it.
func makeEquivalentResult(mut model.Mutation, covered, coverageKnown bool) model.Result {
	return model.Result{
		Mutation: mut, Verdict: model.VerdictEquivalent, Covered: covered, CoverageKnown: coverageKnown,
		Diagnostic: model.Diagnostic{
			ID: verdictRule(model.VerdictEquivalent), Location: mut.Span,
			Statement:   "equivalent: " + mut.EquivalentReason,
			Evidence:    map[string]any{"source_diff": mut.Diff},
			Assumptions: []string{"the equivalence proof is purely syntactic and local to the guard shown; see docs/semantics.md"},
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
	case model.VerdictEquivalent:
		return "MJ-VERDICT-EQUIVALENT"
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
		case model.VerdictEquivalent:
			s.Equivalent++
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
		s.ScoreText = fmt.Sprintf("%.1f%% excluding invalid/timeout/unknown/unsupported/equivalent", s.Score)
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
