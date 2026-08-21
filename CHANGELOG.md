# Changelog

## Unreleased

- Fixed `internal/coverage`: the suffix-ambiguity index decided collisions by comparing covered-line-set content instead of file identity, so two distinct files sharing a path suffix (e.g. two same-shaped functions) with coincidentally identical covered lines could resolve silently instead of being marked ambiguous. Ambiguity is now tracked by which file currently owns each suffix.
- Replaced heuristic English-substring compiler-output matching with structured `go test -json` event classification (see `ISSUES.md`). Fixes a real subtest-attribution gap in the old `--- FAIL: ` regex (it was anchored to line-start and never matched Go's indented subtest result lines) and distinguishes true build/vet failures from same-shaped runtime failures (init() panics, `TestMain` calling `os.Exit`) that were previously indistinguishable by exit code and output text alone.
- Fixed `cmd/mutation-judge`: an abrupt SIGINT/SIGTERM leaked the temporary sandbox directory, since `context.Background()` had no signal handling and Go's default disposition for those signals is immediate termination (deferred cleanup never ran). Now handled with `signal.NotifyContext` and a distinct `exitInterrupted` (130) exit code; proven with a real-subprocess, real-signal regression test.
- Added end-to-end coverage for cgo packages, build-tag-excluded files, `init()` panics, and custom `TestMain` exits (`tests/integration/testdata/`).
- Decided (`docs/decisions/0001-config-parser-scope.md`) to keep the dependency-free strict flat config parser permanently rather than adopt a TOML/YAML library; added YAML native block-list support for list-valued keys as the one concrete gap that decision identified.
- Ran and recorded a manually-reviewed external corpus evaluation (`docs/evaluation.md`) against three real, dependency-free Go libraries.
- Cache write failures (e.g. a full or unwritable `cache_dir`) are no longer silently discarded: they're now a `warnings` evidence field on the report (all three formats) plus a stderr line, deduplicated to one summary message rather than one per mutant. The run still succeeds and the mutant's own result is unaffected — this remains non-fatal, just no longer invisible.
- Added a differential fixture suite (`internal/frontend/differential_test.go`) that reconstructs the full mutated source for a discovered mutant and compares it byte-for-byte against a hand-verified expected string, then confirms it still parses as valid Go. Covers precedence/parenthesization specifically: `&&`/`||` mixed precedence, left-associative `&&` chains at multiple nesting depths, `!(...)` double-parenthesization, and boundary comparisons nested inside a boolean tree. Verified against a deliberate regression (removing the parenthesization wrapping) that 4 of the 8 fixtures fail immediately with a clear diff.
- `cmd/mutation-judge` now appends a durable NDJSON entry to `.mutation-judge/journal.ndjson` on every SIGINT/SIGTERM, independent of `--no-cache`: timestamp, which signal, which phase (baseline vs. mutant execution), patterns, operators, and completed/retained mutant counts where known. Closes the last open correctness-and-trust item.
- Fixed a real platform-dependent misclassification in `internal/runner.classifyEvents`, reported from a macOS run: "build constraints exclude all Go files" produces no JSON events at all on some platforms (the pre-existing fallback path handled that correctly), but on others `go test -json` emits a package-level `FAIL <pkg> [setup failed]` line instead of the `[build failed]` marker the classifier only checked for, so it fell through to KILLED instead of INVALID. Now matches the general `FAIL <pkg> [<reason>]` structure rather than one hardcoded phrase. Added direct unit tests against `classifyEvents` with synthetic events (rather than relying only on a real toolchain's platform-specific behavior) reproducing the exact reported scenario, plus one for an arbitrary unknown future reason word; confirmed both fail against the reverted substring-only check.

## 0.1.2 — 2026-07-18

- Resolved the external code review's confirmed rendering, UTF-8, configuration, atomic-write, cache-race, path-safety, timeout, and report-write defects.
- Refactored analysis orchestration into preparation, baseline, execution, and report-construction phases.
- Added per-mutant progress, backend/operator semantic versioning, exact timeout reproduction commands, indexed coverage lookup, and line-indexed diff rendering.
- Added cancellation-aware incomplete reports and expanded cache, workspace, runner, config, coverage, Git diff, report, and analysis regressions.
- Added the original functional specification, a conformance matrix, the preserved review, and a finding-by-finding resolution record.

## 0.1.1 — 2026-07-17

- Expanded the tutorial into a complete survivor-to-test-repair workflow.
- Added boundary-fixed, arithmetic, selected-test, and generated-source examples.
- Added per-example documentation and an executable `examples/run-all.sh` demonstration.
- Corrected boolean deletion suggestions so they give assignments that mechanically distinguish the original expression from the mutant.

## 0.1.0 — 2026-07-17

- Initial targeted Go mutation-analysis vertical slice.
- Boundary, boolean, and optional arithmetic mutation operators.
- Isolated temporary-workspace execution with baseline coverage.
- Text, versioned JSON, and self-contained HTML reports.
- Strict TOML/YAML configuration, content-addressed caching, and Git diff mode.

## 0.1.3 — 2026-08-06

- Documentation audit: fixed the functional-specification inconsistency that said backend timeouts produce `UNKNOWN` (implementation and rest of the docs already used first-class `TIMEOUT`).
- Packaging: removed macOS `.DS_Store` metadata from the distribution tree.
- Confirmed the v0.1.2 review-resolution fixes still hold; all unit and integration tests pass on Go 1.22+.
