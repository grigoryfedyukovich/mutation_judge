# Issue list

Resolved findings from the 2026-07-18 audit are recorded in [`docs/reviews/review-resolution-v0.1.2.md`](docs/reviews/review-resolution-v0.1.2.md).

## Correctness and trust work

All items in this section are done. Summaries stay here as the audit trail; see git history for the implementations.

- [x] Replace heuristic compiler-output matching with structured `go test -json` event classification and explicit build diagnostics. `internal/runner.GoTest` now classifies from the `-json` event stream: a package-level `FAIL <pkg> [<reason>]` marker (compile, vet, or setup) means INVALID when that package never started a test of its own; a package that fails before any test starts for a non-build reason (init() panic, `TestMain` calling `os.Exit`, ...) means KILLED with no test attributed; a test still "started" when the stream ends is attributed as responsible. The previous English-substring regex is kept only as a last-resort fallback when `go test` rejects the invocation before `test2json` writes JSON. Nested subtest `--- FAIL:` lines are matched (leading whitespace). Classification is per package, so a sibling package's passing tests cannot turn another package's compile failure into a counted KILL on `./...`.
- [x] Add process-signal tests proving temporary-workspace cleanup under abrupt CLI termination. `cmd/mutation-judge/main.go` handles SIGINT/SIGTERM, exit code `130`, sandbox cleanup, and an NDJSON journal entry at `.mutation-judge/journal.ndjson`.
- [x] Add a fixed external corpus evaluation with manually reviewed survivors. See `docs/evaluation.md`, "External corpus evaluation" (2026-08-18).
- [x] Test cgo, build tags, package initialization failures, custom `TestMain`, and unusual toolchain failures (`tests/integration/testdata/`).
- [x] Keep the strict flat TOML/YAML subset permanently; do not adopt a parser library. YAML native block lists for list-valued keys are accepted. See `docs/decisions/0001-config-parser-scope.md`.
- [x] Surface non-fatal cache write failures as `model.Report.Warnings`, in text/HTML, and on stderr.
- [x] Differential fixtures for source replacements and boolean parenthesization (`internal/frontend/differential_test.go`).
- [x] Persistent journal on SIGINT/SIGTERM (baseline vs mutant-execution phase).
- [x] Platform `FAIL <pkg> [setup failed]` (and any other bracketed reason) classified INVALID, not KILLED.
- [x] Multi-package `./...` compile failure classified INVALID even when a sibling package's tests run.
- [x] Cache key and report toolchain fields come from `go env GOVERSION GOOS GOARCH CGO_ENABLED GOFLAGS` (`runner.DetectToolchain`), not `runtime.Version()`.
- [x] `Digest` hashes exactly the file set `CopyModule` places in the sandbox (`sandboxEntries`). Outbound-symlink policy and `vendor/`/`bin/`/`node_modules/` skip remain open (optional expansion below).
- [x] Timeout classification: outer `context.DeadlineExceeded` is definitive; the text fallback matches only `^panic: test timed out after ` on a failed process.

## Performance work

- [x] Measure `Digest` / `CopyModule` cost on large synthetic modules. See `docs/performance.md`.
- [x] Linux copy-on-write via `FICLONE`, always falling back to a full copy. Clone *success* path is not verified in this project's kernel; macOS `clonefile(2)` is deferred.
- [x] Dependency-graph-guided test scoping (`--narrow-test-scope`, default off). Not coverage-guided; see `docs/performance.md`. Cache key includes the patterns actually used.
- [x] Parallel isolated workers (`--workers N`, default 1) with deterministic output ordering. Worker count is not part of the cache key.

## Optional feature expansion

- [ ] Distributed CI manifests: split one run's mutants across separate jobs/machines and merge JSON reports. Distinct from `--workers`, which parallelizes within one process. See `docs/performance.md`.
- [x] SARIF and GitHub annotation rendering. `--format sarif` (SARIF 2.1.0) and `--format github` (workflow-command annotations on stdout). Only `SURVIVED`, `TIMEOUT`, and `UNKNOWN` produce findings. See `docs/tutorial.md` section 15.
- [x] Targeted operators for error returns, switch cases, loops, and channels (`errorreturn`, `switch`, `loop`, `channel`; all opt-in). See `docs/semantics.md`. Further operators are a separate item below.
- [x] Cross-run comparison and score trend. `compare` diffs two `--format json` reports into six buckets (`new_survivors`, `fixed_survivors`, `still_open`, `reclassified`, `removed_mutants`, `unchanged_count`) plus `likely_shifted`. `record`/`trend` keep `.mutation-judge/history.ndjson`. Text and JSON only; HTML visualization of two reports is still open.
- [x] Conservative equivalent-mutant suppression for locally provable guarded comparisons. Implemented for the exact shape this item named: a boundary comparison dominated by an `if X != Y { return X < Y }`-shaped guard on the same two operands is classified `EQUIVALENT` and never executed (`internal/frontend.detectGuardedComparison`). Confirmed against the real `sort.Slice` comparator in `internal/frontend.Discover` (`docs/evaluation.md`, "Guarded sort comparisons"). Deliberately narrow -- see `docs/limitations.md` limitation 7 and `docs/semantics.md`.
- [ ] Cross-run HTML comparison (text and JSON `compare` already ship).
- [ ] Additional operators beyond the four opt-in families already implemented. Same timeout-safety rule: never generate a mutant whose expected verdict is an uninformative `TIMEOUT`.
- [ ] Coverage-attributed per-test selection. Distinct from `--narrow-test-scope` (import graph, not `-coverprofile`).
- [ ] Outbound-symlink sandbox policy. `CopyModule` recreates symlink objects by target string; a policy for links that escape the module root is not specified.
- [ ] Skip `vendor/` / `bin/` / `node_modules/` in `CopyModule`/`Digest` when doing so cannot change a verdict (depends on `-mod` and what tests observe).
- [ ] macOS `clonefile(2)` sandbox clone. Linux `FICLONE` exists; `TestTryReflinkClonesOrCleanlyDeclines` skips where the filesystem cannot clone.
