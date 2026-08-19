# Changelog

## Unreleased

- Fixed `internal/coverage`: the suffix-ambiguity index decided collisions by comparing covered-line-set content instead of file identity, so two distinct files sharing a path suffix (e.g. two same-shaped functions) with coincidentally identical covered lines could resolve silently instead of being marked ambiguous. Ambiguity is now tracked by which file currently owns each suffix.
- Replaced heuristic English-substring compiler-output matching with structured `go test -json` event classification (see `ISSUES.md`). Fixes a real subtest-attribution gap in the old `--- FAIL: ` regex (it was anchored to line-start and never matched Go's indented subtest result lines) and distinguishes true build/vet failures from same-shaped runtime failures (init() panics, `TestMain` calling `os.Exit`) that were previously indistinguishable by exit code and output text alone.

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
