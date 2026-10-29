package analysis

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/example/mutation-judge/internal/cache"
	"github.com/example/mutation-judge/internal/config"
	covermap "github.com/example/mutation-judge/internal/coverage"
	"github.com/example/mutation-judge/internal/frontend"
	"github.com/example/mutation-judge/internal/gitdiff"
	"github.com/example/mutation-judge/internal/model"
	"github.com/example/mutation-judge/internal/resultspool"
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
	// afterSnapshot is a test seam for verifying post-capture host edits.
	afterSnapshot func()
}

type Request struct {
	CWD      string
	Patterns []string
	Config   config.Config
}

type preparedAnalysis struct {
	root            string
	workRel         string
	sandbox         string
	cleanup         func()
	mutants         []model.Mutation
	discovered      int
	parsingMS       int64
	sourceDigest    string
	environment     []string // frozen child process environment, excluding ambient GOWORK
	environmentHash string   // never expose raw environment values in reports
	cacheSafe       bool     // false for untracked external local replacements / flag files
	cacheWarning    string
	backendName     string
	backendVersion  string
	toolchain       runner.ToolchainInfo
	filePackage     map[string]string   // relative file path -> owning package import path
	testScopes      map[string][]string // package import path -> observing test packages; resolved for scope/coverage selection
	pkgs            []workspace.Package // resolved go test package universe; also used for per-test coverage and cache identity
	perTestCoverage covermap.PerTest    // zero value (Len()==0) unless CoverageTestSelection is on and succeeded
}

// Analyze preserves the in-memory API for integrations requiring direct Results access.
func (e Engine) Analyze(ctx context.Context, req Request) (model.Report, error) {
	return e.analyze(ctx, req, nil)
}

// AnalyzeSpool persists completed results incrementally and returns report
// metadata without an in-memory Results slice. Callers must Close the spool.
// Only the CLI's JSON/text streaming renderers currently consume this form.
func (e Engine) AnalyzeSpool(ctx context.Context, req Request) (model.Report, *resultspool.Spool, error) {
	spool, err := resultspool.New()
	if err != nil {
		return model.Report{}, nil, err
	}
	r, err := e.analyze(ctx, req, spool)
	if err != nil {
		_ = spool.Close()
		return model.Report{}, nil, err
	}
	return r, spool, nil
}

func (e Engine) analyze(ctx context.Context, req Request, spool *resultspool.Spool) (model.Report, error) {
	started := time.Now()
	if req.Config.TotalTimeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, req.Config.TotalTimeout)
		defer cancel()
	}
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
	// Preparation has an independent deadline, even when the caller uses
	// a signal-only context. Its budget includes toolchain detection,
	// dependency resolution, snapshot creation and mutation discovery.
	prepCtx := ctx
	prepCancel := func() {}
	if req.Config.PreparationTimeout > 0 {
		prepCtx, prepCancel = context.WithTimeout(ctx, req.Config.PreparationTimeout)
	}
	defer prepCancel()
	toolchain, err := runner.DetectToolchain(prepCtx)
	if err != nil {
		return model.Report{}, err
	}

	prepared, err := e.prepareContext(prepCtx, req, toolchain)
	if err != nil {
		return model.Report{}, err
	}
	defer prepared.cleanup()
	// The preparation deadline must not curtail tests once discovery is
	// complete; the entire analysis deadline (if set) remains active.
	prepCancel()

	coverage, coverageKnown, baselineMS, baselineWarning, err := e.runBaseline(ctx, req, prepared)
	if err != nil {
		return model.Report{}, err
	}

	var perTestWarning string
	if req.Config.CoverageTestSelection && !coverageKnown {
		// Missing or failing aggregate coverage cannot justify pruning tests.
		// Individual instrumented runs may not reflect normal test behavior.
		perTestWarning = "coverage-test-selection disabled for this run: baseline coverage is unavailable"
	} else if req.Config.CoverageTestSelection {
		perTest, ptErr := e.buildPerTestCoverage(ctx, req, prepared)
		if ptErr != nil {
			// Never a hard failure: coverage-test selection is purely a
			// speed optimization layered on top of whatever test scope
			// would otherwise apply (the full pattern set, or
			// --narrow-test-scope's package-level narrowing). Any
			// uncertainty about it -- a test that fails when run in
			// isolation, a listing failure, a bad profile -- falls back
			// to that wider, already-safe scope for every mutant this
			// run, exactly like mutantTestScope's own fallback rule,
			// rather than risking a single flaky or order-dependent
			// test silently narrowing some mutant's run unsafely.
			perTestWarning = fmt.Sprintf("coverage-test-selection disabled for this run: %v", ptErr)
		} else {
			prepared.perTestCoverage = perTest
		}
	}

	var results []model.Result
	var streamedCount int
	var streamedSummary model.Summary
	var complete bool
	var executionMS int64
	var warnings []string
	if spool == nil {
		results, complete, executionMS, warnings, err = e.executeMutants(ctx, req, prepared, coverage, coverageKnown)
	} else {
		streamedCount, streamedSummary, complete, executionMS, warnings, err = e.executeMutantsSpool(ctx, req, prepared, coverage, coverageKnown, spool)
	}
	if err != nil {
		return model.Report{}, err
	}
	if perTestWarning != "" {
		warnings = append([]string{perTestWarning}, warnings...)
	}
	if baselineWarning != "" {
		warnings = append([]string{baselineWarning}, warnings...)
	}
	if prepared.cacheWarning != "" {
		warnings = append([]string{prepared.cacheWarning}, warnings...)
	}
	deadlineExceeded := req.Config.TotalTimeout > 0 && ctx.Err() == context.DeadlineExceeded
	if deadlineExceeded {
		complete = false
		warnings = append(warnings, "total analysis deadline exceeded; partial results cannot establish complete mutation coverage")
	}
	r := buildReport(e.Version, req, prepared, results, complete, baselineMS, executionMS, warnings, started)
	if spool != nil {
		r.Results = nil
		r.Summary = streamedSummary
		r.Bounds["completed_mutants"] = streamedCount
	}
	if deadlineExceeded {
		r.Bounds["analysis_deadline_exceeded"] = true
	}
	return r, nil
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

// mutantTestRun returns the `-run` regular expression to use for one
// mutant's execution. It is req.Config.TestRun unchanged unless all of
// the following hold, in which case it narrows to ordinary tests known to
// reach the mutant's own span, together with runnable examples and fuzz
// targets from potentially observing packages:
//
//   - config.CoverageTestSelection is enabled and p.perTestCoverage was
//     successfully built (see buildPerTestCoverage);
//   - the person has not already set their own --test-run (narrowing on
//     top of an existing, arbitrary user regular expression would mean
//     either intersecting two regexes -- not generally expressible as a
//     single regex -- or silently overriding what the person asked for;
//     neither is acceptable, so this feature simply does not engage);
//   - CoveringTests reports a *known*, *non-empty* result. An unknown
//     result (this span's file was never profiled, e.g. a build-tagged
//     file coverage never touched) falls back exactly like
//     mutantTestScope's own fallback. A known-but-empty result is
//     never turned into `-run '^$'`: an empty `-run` pattern with a
//     Go regexp does not mean "match nothing", it errors or matches
//     unpredictably (Go's os/exec + testing flag parsing does not
//     special-case it into a deliberate empty selection the way the
//     zero value of TestRun does), and either way running zero tests
//     for a mutant is never the correct behavior -- something must
//     always execute so the mutant gets a real, if excludable, verdict
//     rather than an ambiguous non-run.
func mutantTestRun(req Request, p preparedAnalysis, mut model.Mutation) string {
	if req.Config.TestRun != "" || !req.Config.CoverageTestSelection {
		return req.Config.TestRun
	}
	// A package with no dependency analysis or any missing observer profile
	// cannot justify eliminating tests. In that case run the requested suite.
	owner, ok := p.filePackage[mut.Span.File]
	if !ok {
		return req.Config.TestRun
	}
	tests, known := p.perTestCoverage.CoveringTestsForPackages(mut.Span.File, mut.Span.StartLine, mut.Span.EndLine, p.testScopes[owner])
	if !known || len(tests) == 0 {
		return req.Config.TestRun
	}
	// A normal `go test` also runs examples and fuzz seed targets. Their
	// coverage is not included in ordinary Test profiles, so keep all of
	// them in potentially observing packages even if they never appear in
	// the computed Test-only covering set (F06).
	tests = append(tests, p.perTestCoverage.AlwaysRunForPackages(p.testScopes[owner])...)
	quoted := make([]string, len(tests))
	for i, name := range tests {
		quoted[i] = regexp.QuoteMeta(name)
	}
	return "^(" + strings.Join(quoted, "|") + ")$"
}

func (e Engine) prepare(req Request, toolchain runner.ToolchainInfo) (preparedAnalysis, error) {
	return e.prepareContext(context.Background(), req, toolchain)
}

func (e Engine) prepareContext(ctx context.Context, req Request, toolchain runner.ToolchainInfo) (preparedAnalysis, error) {
	root, err := workspace.ModuleRootContext(ctx, req.CWD)
	if err != nil {
		return preparedAnalysis{}, err
	}
	workRel, err := filepath.Rel(root, req.CWD)
	if err != nil || workRel == ".." || strings.HasPrefix(workRel, ".."+string(filepath.Separator)) {
		return preparedAnalysis{}, fmt.Errorf("working directory must be inside module root %s", root)
	}
	// Pin the Git index and revision before capturing source inputs. Git
	// selection later uses the snapshot, never the mutable host worktree.
	var gitBase *gitdiff.SnapshotBase
	if req.Config.ChangedBase != "" {
		gitBase, err = gitdiff.CaptureSnapshotBase(ctx, root, req.Config.ChangedBase)
		if err != nil {
			return preparedAnalysis{}, err
		}
		defer gitBase.Close()
	}
	// Capture inputs FIRST: all subsequent package, test-scope, and mutation
	// discovery must use these same bytes. Verify that no edit overlapped
	// the copy before admitting the snapshot to an experiment.
	snapshot, cleanup, err := workspace.CopyModuleContext(ctx, root, req.Config.CacheDir)
	if err != nil {
		return preparedAnalysis{}, err
	}
	preparedOK := false
	defer func() {
		if !preparedOK {
			cleanup()
		}
	}()
	if err = workspace.VerifyModuleCopyContext(ctx, root, snapshot, req.Config.CacheDir); err != nil {
		return preparedAnalysis{}, err
	}
	if gitBase != nil {
		if err := gitBase.VerifyIndex(); err != nil {
			return preparedAnalysis{}, err
		}
	}
	if e.afterSnapshot != nil {
		e.afterSnapshot()
	}

	parseStart := time.Now()
	var changed map[string]map[int]bool
	if gitBase != nil {
		changed, err = gitBase.ChangedLinesFromSnapshotContext(ctx, snapshot)
		if err != nil {
			return preparedAnalysis{}, err
		}
	}
	// All local replacements, including out-of-tree dependencies, must be
	// redirected before asking Go to resolve packages or test dependencies.
	localReplacement, err := workspace.IsolateLocalReplacements(ctx, root, snapshot, req.Config.CacheDir)
	if err != nil {
		return preparedAnalysis{}, fmt.Errorf("isolate local replacements: %w", err)
	}
	workDir := filepath.Join(snapshot, workRel)
	pkgs, err := workspace.ListPackagesContext(ctx, workDir, req.Patterns)
	if err != nil {
		return preparedAnalysis{}, err
	}
	files, err := workspace.SourceFiles(snapshot, pkgs)
	if err != nil {
		return preparedAnalysis{}, err
	}
	filePackage, err := fileOwningPackages(snapshot, pkgs)
	if err != nil {
		return preparedAnalysis{}, err
	}
	var testScopes map[string][]string
	if req.Config.NarrowTestScope || req.Config.CoverageTestSelection {
		testScopes, err = workspace.TestScopesContext(ctx, workDir, req.Patterns)
		if err != nil {
			return preparedAnalysis{}, err
		}
	}

	opset := make(map[string]bool, len(req.Config.Operators))
	for _, op := range req.Config.Operators {
		opset[op] = true
	}
	mutants, discovered, err := frontend.DiscoverLimited(snapshot, files, frontend.Options{
		Operators:        opset,
		IncludeGenerated: req.Config.IncludeGenerated,
		ChangedLines:     changed,
	}, req.Config.MaxMutants)
	if err != nil {
		return preparedAnalysis{}, err
	}
	// Frontend discovery can be CPU-intensive; even if it finishes after
	// its deadline, do not admit the resulting snapshot as a valid run.
	if err := ctx.Err(); err != nil {
		return preparedAnalysis{}, fmt.Errorf("preparation deadline or cancellation: %w", err)
	}
	parsingMS := time.Since(parseStart).Milliseconds()

	// GOFLAGS -modfile and -overlay can read arbitrary files outside the
	// sandbox; their paths alone are not a fingerprint of their contents.
	untrackedFlags := untrackedGoFlagInputs(toolchain.GoFlags)
	cacheSafe := !localReplacement && !untrackedFlags && toolchain.ConfigUnsafeReason == ""
	var cacheWarning string
	if req.Config.Cache && !cacheSafe {
		cacheWarning = "mutation result caching disabled: untracked execution configuration (local replacements, external Go flag files, or persistent Go configuration)"
		if toolchain.ConfigUnsafeReason != "" {
			cacheWarning += ": " + toolchain.ConfigUnsafeReason
		}
	}
	environment := runner.EffectiveTestEnvironment(os.Environ())
	environmentHash := digestEnvironment(environment)
	sourceDigest, err := workspace.Digest(snapshot, req.Config.CacheDir)
	if err != nil {
		return preparedAnalysis{}, err
	}
	if err := ctx.Err(); err != nil {
		return preparedAnalysis{}, fmt.Errorf("preparation deadline or cancellation: %w", err)
	}
	// The snapshot is a template, never a runnable workspace. A test may
	// write anywhere in its sandbox, so no sandbox is reused.
	backendName, backendVersion := backendIdentity(e.Backend)
	preparedOK = true
	return preparedAnalysis{
		root: root, workRel: filepath.ToSlash(workRel), sandbox: snapshot, cleanup: cleanup,
		mutants: mutants, discovered: discovered, parsingMS: parsingMS, sourceDigest: sourceDigest,
		environment: environment, environmentHash: environmentHash, cacheSafe: cacheSafe, cacheWarning: cacheWarning,
		backendName: backendName, backendVersion: backendVersion, toolchain: toolchain,
		filePackage: filePackage, testScopes: testScopes, pkgs: pkgs,
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

// runBaseline validates the exact uninstrumented test command used to judge
// mutants. Coverage is auxiliary evidence, not permission to accept a suite
// which would fail without instrumentation. Both invocations begin from
// independent clean copies so tests that change fixtures cannot affect each
// other (F01/R01). The reported baseline time includes both invocations.
func (e Engine) runBaseline(ctx context.Context, req Request, p preparedAnalysis) (covermap.Map, bool, int64, string, error) {
	started := time.Now()
	elapsed := func() int64 { return time.Since(started).Milliseconds() }
	// Match mutant runs: no CoverageOut or other extra go test flags.
	sandbox, cleanup, err := workspace.CopyModule(p.sandbox, req.Config.CacheDir)
	if err != nil {
		return covermap.Map{}, false, elapsed(), "", fmt.Errorf("create clean baseline sandbox: %w", err)
	}
	baseline := e.Backend.Run(ctx, runner.Request{
		Root: sandbox, WorkRel: p.workRel, Patterns: req.Patterns,
		TestRun: req.Config.TestRun, Timeout: req.Config.Timeout, Environment: p.environment,
		GoVersion: p.toolchain.GoVersion,
	})
	cleanup()
	if baseline.Verdict != model.VerdictSurvived {
		return covermap.Map{}, false, elapsed(), "", fmt.Errorf("uninstrumented baseline tests must pass before mutation analysis (verdict %s):\n%s", baseline.Verdict, baseline.Output)
	}
	if err := ctx.Err(); err != nil {
		return covermap.Map{}, false, elapsed(), "", fmt.Errorf("baseline canceled: %w", err)
	}

	// Profiling is optional evidence, not the authoritative test command.
	// Errors and instrumentation-dependent failures cannot establish coverage;
	// retain normal mutant execution with unknown coverage instead.
	sandbox, cleanup, err = workspace.CopyModule(p.sandbox, req.Config.CacheDir)
	if err != nil {
		if ctx.Err() != nil {
			return covermap.Map{}, false, elapsed(), "", ctx.Err()
		}
		return covermap.Map{}, false, elapsed(), fmt.Sprintf("baseline coverage unavailable: cannot create profiling sandbox: %v", err), nil
	}
	defer cleanup()
	coveragePath := filepath.Join(sandbox, ".mutation-judge", "coverage.out")
	if err := os.MkdirAll(filepath.Dir(coveragePath), 0o755); err != nil {
		return covermap.Map{}, false, elapsed(), fmt.Sprintf("baseline coverage unavailable: %v", err), nil
	}
	profiled := e.Backend.Run(ctx, runner.Request{
		Root: sandbox, WorkRel: p.workRel, Patterns: req.Patterns,
		TestRun: req.Config.TestRun, Timeout: req.Config.Timeout, CoverageOut: coveragePath, Environment: p.environment,
		GoVersion: p.toolchain.GoVersion,
	})
	if err := ctx.Err(); err != nil {
		return covermap.Map{}, false, elapsed(), "", fmt.Errorf("baseline coverage canceled: %w", err)
	}
	if profiled.Verdict != model.VerdictSurvived {
		return covermap.Map{}, false, elapsed(), fmt.Sprintf("baseline coverage unavailable: instrumented tests returned %s (uninstrumented baseline passed)", profiled.Verdict), nil
	}
	coverage, err := covermap.Parse(coveragePath, sandbox)
	if err != nil {
		return covermap.Map{}, false, elapsed(), fmt.Sprintf("baseline coverage unavailable: invalid coverage profile: %v", err), nil
	}
	return coverage, true, elapsed(), "", nil
}

// buildPerTestCoverage lists all runnable units, preserves examples and fuzz
// targets for every selected mutant, and runs every top-level Test function in every
// package (within p.pkgs) that has its own test files individually,
// each with its own coverage profile, to determine exactly which
// tests reach which lines -- the "much heavier machinery" a plain
// aggregate -coverprofile run cannot provide (it only says a line ran
// *somewhere* in the full suite, never by which test), and the reason
// --narrow-test-scope is dependency-graph-guided rather than
// coverage-guided by default (see docs/performance.md). This is that
// heavier path, opt-in via --coverage-test-selection.
//
// Any single failure here -- a package that fails to list its tests,
// or a test that fails when run in isolation even though the full
// baseline (already validated by runBaseline) passed -- aborts the
// whole attempt and returns an error. It does not try to salvage
// per-package partial results: a test failing alone when it passed as
// part of the full suite is itself a signal that this package's tests
// are not safely isolatable (shared mutable state, execution-order
// dependence, or similar), which calls the soundness of *any*
// coverage collected from it into question, not just that one test's.
// The caller (Analyze) treats any error as reason to disable
// coverage-test selection for the whole run, not to fail the analysis
// -- this is a speed optimization layered on top of an already-safe
// test scope, never a source of correctness risk on its own.
func (e Engine) buildPerTestCoverage(ctx context.Context, req Request, p preparedAnalysis) (covermap.PerTest, error) {
	var per covermap.PerTest
	// Explicit package import paths avoid -coverpkg's default of instrumenting
	// only the package under test, which loses production coverage when an
	// integration test in package B imports and exercises package A.
	instrument := make([]string, 0, len(p.pkgs))
	for _, pkg := range p.pkgs {
		if pkg.ImportPath != "" {
			instrument = append(instrument, pkg.ImportPath)
		}
	}
	sort.Strings(instrument)
	for _, pkg := range p.pkgs {
		if pkg.Error != nil && pkg.Error.Err != "" || !pkg.HasOwnTests() {
			continue
		}
		// Test initialization during enumeration may also write fixtures.
		// Never let that change the inputs of an individual test.
		listing, cleanup, err := workspace.CopyModule(p.sandbox, req.Config.CacheDir)
		if err != nil {
			return covermap.PerTest{}, fmt.Errorf("create listing sandbox for %s: %w", pkg.ImportPath, err)
		}
		units, err := runner.ListRunnableUnitsWithEnvironment(ctx, listing, p.workRel, pkg.ImportPath, req.Config.Timeout, p.environment)
		cleanup()
		if err != nil {
			return covermap.PerTest{}, fmt.Errorf("listing runnable units in %s: %w", pkg.ImportPath, err)
		}
		for _, name := range units.AlwaysRun {
			per.AddAlwaysRunInPackage(pkg.ImportPath, name)
		}
		for _, name := range units.Tests {
			if err := ctx.Err(); err != nil {
				return covermap.PerTest{}, err
			}
			sandbox, cleanup, err := workspace.CopyModule(p.sandbox, req.Config.CacheDir)
			if err != nil {
				return covermap.PerTest{}, fmt.Errorf("create profiling sandbox for %s: %w", name, err)
			}
			profilePath := filepath.Join(sandbox, ".mutation-judge", "pertest-coverage.out")
			if err := os.MkdirAll(filepath.Dir(profilePath), 0o755); err != nil {
				cleanup()
				return covermap.PerTest{}, err
			}
			result := e.Backend.Run(ctx, runner.Request{
				Root: sandbox, WorkRel: p.workRel, Patterns: []string{pkg.ImportPath},
				TestRun: "^" + regexp.QuoteMeta(name) + "$", Timeout: req.Config.Timeout, CoverageOut: profilePath, CoveragePackages: instrument, Environment: p.environment,
				GoVersion: p.toolchain.GoVersion,
			})
			if result.Verdict != model.VerdictSurvived {
				cleanup()
				return covermap.PerTest{}, fmt.Errorf("%s (in %s) must pass when run in isolation (verdict %s):\n%s", name, pkg.ImportPath, result.Verdict, result.Output)
			}
			m, err := covermap.Parse(profilePath, sandbox)
			cleanup()
			if err != nil {
				return covermap.PerTest{}, fmt.Errorf("parsing coverage profile for %s: %w", name, err)
			}
			per.AddInPackage(pkg.ImportPath, name, m)
		}
	}
	return per, nil
}

// runOneMutant clones a fresh writable sandbox for every cache miss; all
// workers share only the read-only snapshot and the cache store.
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
// does directly (Patterns comes from the computed test scope, and the
// actual `-run` value comes from mutantTestRun, not directly from
// Config -- both are already captured separately in the cache key, via
// the scope string and the testRun string respectively, since either
// can differ per mutant even when c.TestRun itself does not: that's
// exactly what --narrow-test-scope and --coverage-test-selection do).
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

// digestEnvironment binds cached verdicts to all actual child-process variables.
// The serialized cache identity contains only this digest, not passwords or
// tokens; the environment strings themselves are never written into reports.
func digestEnvironment(env []string) string {
	h := sha256.New()
	for _, entry := range env {
		_, _ = h.Write([]byte(entry))
		_, _ = h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil))
}

// Go's -modfile and -overlay flags reference file contents that can change
// without changing the flag value. Conservatively disable caching instead
// of pretending that hashing the environment alone tracks those files.
func untrackedGoFlagInputs(flags string) bool {
	for _, token := range strings.Fields(flags) {
		if token == "-modfile" || strings.HasPrefix(token, "-modfile=") ||
			token == "-overlay" || strings.HasPrefix(token, "-overlay=") ||
			token == "-toolexec" || strings.HasPrefix(token, "-toolexec=") {
			return true
		}
	}
	return false
}

// cacheExecutionSchema changes whenever the serialized execution identity changes.
// /v5 adds resolved persistent Go environment configuration to cache identity.
// Older entries omitted GOENV/CGO_* settings and are not safe to reuse.
const cacheExecutionSchema = "mutation-judge-execution/v5"

// mutantCacheKey binds a verdict to the *resolved execution*, not just the
// textual patterns. Go interprets ./... relative to WorkRel: the same source
// mutation may be KILLED from the module root but SURVIVED from a subfolder.
// A JSON object preserves field boundaries, pattern boundaries and scope
// ordering; comma-joining the arguments would allow unrelated inputs to
// produce the same serialized key. Versioning makes the old unsafe keys cold.
func mutantCacheKey(version string, p preparedAnalysis, cfgJSON []byte, mut model.Mutation, scope []string, testRun string) (string, error) {
	targets := make([]string, 0, len(p.pkgs))
	for _, pkg := range p.pkgs {
		if pkg.ImportPath != "" {
			targets = append(targets, pkg.ImportPath)
		}
	}
	sort.Strings(targets)
	identity := struct {
		Schema          string          `json:"schema"`
		ToolVersion     string          `json:"tool_version"`
		OperatorVersion string          `json:"operator_version"`
		Toolchain       string          `json:"toolchain"`
		SourceDigest    string          `json:"source_digest"`
		EnvironmentHash string          `json:"environment_hash"`
		Config          json.RawMessage `json:"config"`
		BackendName     string          `json:"backend_name"`
		BackendVersion  string          `json:"backend_version"`
		MutationID      string          `json:"mutation_id"`
		Replacement     string          `json:"replacement"`
		WorkingDir      string          `json:"working_directory"`
		Patterns        []string        `json:"patterns"`
		ResolvedTargets []string        `json:"resolved_targets"`
		TestRun         string          `json:"test_run"`
	}{
		Schema: cacheExecutionSchema, ToolVersion: version,
		OperatorVersion: frontend.SemanticsVersion, Toolchain: p.toolchain.Key(),
		SourceDigest: p.sourceDigest, EnvironmentHash: p.environmentHash, Config: cfgJSON,
		BackendName: p.backendName, BackendVersion: p.backendVersion,
		MutationID: mut.ID, Replacement: mut.Replacement,
		WorkingDir: p.workRel, Patterns: scope, ResolvedTargets: targets,
		TestRun: testRun,
	}
	b, err := json.Marshal(identity)
	if err != nil {
		return "", fmt.Errorf("marshal mutant execution cache identity: %w", err)
	}
	return cache.Key(string(b)), nil
}

func (e Engine) runOneMutant(ctx context.Context, req Request, p preparedAnalysis, store cache.Store, cfgJSON []byte, mut model.Mutation, coverage covermap.Map, coverageKnown bool) (result model.Result, executed bool, cachePutErr error, hardErr error) {
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
	testRun := mutantTestRun(req, p, mut)
	key, err := mutantCacheKey(e.Version, p, cfgJSON, mut, scope, testRun)
	if err != nil {
		return model.Result{}, false, nil, err
	}
	backendResult, hit := store.Get(key)
	if !hit {
		// Every mutant starts from the same pristine snapshot, including
		// when multiple jobs pass through the same worker goroutine.
		sandbox, cleanup, err := workspace.CopyModule(p.sandbox, req.Config.CacheDir)
		if err != nil {
			return model.Result{}, false, nil, fmt.Errorf("create clean sandbox for %s: %w", mut.ID, err)
		}
		defer cleanup()
		_, err = workspace.ApplyChecked(sandbox, mut.Span.File, mut.Span.StartByte, mut.Span.EndByte, mut.Original, mut.Replacement)
		if err != nil {
			return model.Result{}, false, nil, err
		}
		backendResult = e.Backend.Run(ctx, runner.Request{
			Root: sandbox, WorkRel: p.workRel, Patterns: scope,
			TestRun: testRun, Timeout: req.Config.Timeout, Environment: p.environment,
			GoVersion: p.toolchain.GoVersion,
		})
		executed = true
		// Only the disposable sandbox is mutated; no restoration is
		// needed, and arbitrary fixture changes cannot reach later tests.
		// Cancellation may arrive while the backend is shutting down. Never
		// save even a seemingly definitive result from an interrupted run.
		// Transient UNKNOWN/TIMEOUT verdicts are also ineligible for reuse.
		if putErr := store.PutContext(ctx, key, backendResult); putErr != nil {
			cachePutErr = putErr
		}
	}
	result = makeResult(mut, backendResult, hit, covered, known)
	// Report the actual working directory and command for this verdict,
	// including narrowed scopes; the top-level command is only user intent.
	result.Diagnostic.Evidence["test_working_directory"] = p.workRel
	result.Diagnostic.Evidence["test_command"] = testCommandForRun(req.Config.Timeout, testRun, scope)
	return result, executed, cachePutErr, nil
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
	store := cache.Store{Dir: cacheDir, Enabled: req.Config.Cache && p.cacheSafe}
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
		result, executed, cachePutErr, hardErr := e.runOneMutant(ctx, req, p, store, cfgJSON, mut, coverage, coverageKnown)
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

// executeMutantsParallel dispatches independent mutations to bounded workers.
// Each uncached mutation clones the same immutable prepared snapshot; worker
// goroutines do not own or reuse writable trees. Completion order never
// changes the deterministic ordering of the report's results.
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
	store := cache.Store{Dir: cacheDir, Enabled: req.Config.Cache && p.cacheSafe}

	workers := req.Config.Workers
	if workers > len(p.mutants) {
		workers = len(p.mutants) // no point creating more sandboxes than there are mutants to run
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
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
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
				result, executed, cachePutErr, err := e.runOneMutant(innerCtx, req, p, store, cfgJSON, mut, coverage, coverageKnown)
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
		}()
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
	// An interruption during the final in-flight mutation is still an
	// incomplete experiment even if every dispatched result arrived.
	complete := ctx.Err() == nil
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
	resolved := make([]string, 0, len(p.pkgs))
	for _, pkg := range p.pkgs {
		resolved = append(resolved, pkg.ImportPath)
	}
	sort.Strings(resolved)
	return model.Report{
		SchemaVersion:   model.SchemaVersion,
		ToolVersion:     toolVersion,
		GoVersion:       p.toolchain.GoVersion,
		GeneratedAt:     time.Now().UTC(),
		Complete:        complete,
		Patterns:        append([]string(nil), req.Patterns...),
		EffectiveConfig: req.Config.AsMap(),
		Bounds: map[string]any{
			"max_mutants":                req.Config.MaxMutants,
			"per_mutant_timeout":         req.Config.Timeout.String(),
			"discovered_before_bound":    p.discovered,
			"retained_after_bound":       len(p.mutants),
			"completed_mutants":          len(results),
			"backend_name":               p.backendName,
			"backend_version":            p.backendVersion,
			"operator_semantics":         frontend.SemanticsVersion,
			"test_command":               testCommand(req.Config, req.Patterns),
			"test_working_directory":     p.workRel,
			"resolved_test_packages":     resolved,
			"execution_environment_hash": p.environmentHash,
			"effective_go_config_hash":   p.toolchain.EffectiveConfigHash,
			"execution_inputs_complete":  p.cacheSafe,
			"goos":                       p.toolchain.GOOS,
			"goarch":                     p.toolchain.GOARCH,
			"cgo_enabled":                p.toolchain.CgoEnabled,
			"goflags":                    p.toolchain.GoFlags,
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
	if len(rr.Assertions) > 0 {
		evidence["responsible_assertions"] = rr.Assertions
	}
	if len(rr.Panics) > 0 {
		evidence["responsible_panics"] = rr.Panics
	}
	if rr.OutputTruncated {
		evidence["output_truncated"] = true
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
		Mutation: mut, Verdict: rr.Verdict, Responsible: rr.Tests, Assertions: rr.Assertions, Panics: rr.Panics,
		Covered: covered, CoverageKnown: coverageKnown,
		Cached: cached, DurationMS: rr.DurationMS, Output: rr.Output, OutputTruncated: rr.OutputTruncated,
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

// testCommandForRun records the effective mutant command including the
// GoTest backend's structured output flag; baseline coverage flags do not
// apply to mutant executions.
func testCommandForRun(timeout time.Duration, testRun string, patterns []string) []string {
	cmd := []string{"go", "test", "-json", "-count=1", "-timeout", timeout.String()}
	if testRun != "" {
		cmd = append(cmd, "-run", testRun)
	}
	return append(cmd, patterns...)
}

func backendIdentity(b runner.Backend) (string, string) {
	if described, ok := b.(runner.DescribedBackend); ok {
		return described.Name(), described.Version()
	}
	return fmt.Sprintf("%T", b), "unspecified"
}
