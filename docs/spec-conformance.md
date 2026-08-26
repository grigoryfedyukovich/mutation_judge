# Specification conformance

This document maps the repository to [the functional specification](../SPECIFICATION.md). It is intentionally explicit about implementation choices and remaining work.

## Implemented vertical slice

| Specification area | Status | Evidence |
|---|---|---|
| Curated Go AST mutations | Implemented | Boundary, boolean deletion/negation/literal, opt-in arithmetic, and four further opt-in operators (`errorreturn`, `switch`, `loop`, `channel`) in `internal/frontend`. |
| One mutant at a time | Implemented | Atomic apply/run/restore in one isolated temporary module copy. |
| Test classification | Implemented | `KILLED`, `SURVIVED`, `INVALID`, `TIMEOUT`, `UNKNOWN`, `UNSUPPORTED`, and `EQUIVALENT` model values. |
| Responsible tests | Implemented with documented limits | Standard `--- FAIL:` events are extracted and sorted. |
| Exact survivor diff and suggestion | Implemented | Every surviving result carries a unified diff and operator-specific scenario. |
| Git diff mode | Implemented | Zero-context changed-line mapping with deleted-file and zero-count handling. |
| Coverage explanation | Implemented | Baseline statement coverage annotates each mutation span. |
| Text, JSON, HTML, SARIF, GitHub annotations | Implemented | JSON schema `mutation-judge.report/v1`; embedded maintainable HTML template; SARIF 2.1.0 (`internal/report/sarif.go`) and GitHub workflow-command annotations (`internal/report/github.go`) for `SURVIVED`/`TIMEOUT`/`UNKNOWN` verdicts. |
| Strict project configuration | Implemented as a documented subset | Flat scalar/list TOML or YAML syntax, strict unknown-key rejection, CLI overrides, `--print-config`. |
| Content-addressed cache | Implemented | Tool version, operator-semantics version, source digest, effective config, backend identity/version, and mutant identity. |
| Deterministic bounds | Implemented | Reports distinguish candidates discovered before and retained after `max_mutants`. |
| Backend abstraction | Implemented | Narrow `runner.Backend`, optional backend name/version descriptor, deterministic fakes in tests. |
| Local/privacy boundary | Implemented | No shell interpolation or source upload; normal execution requires no network. |
| CI policy exit | Implemented | Successful analysis returns zero unless an explicit score policy fails. |
| Three running examples | Exceeded | Eleven examples plus `examples/run-all.sh`. |
| Real-world evaluation | Implemented, bounded | Self-hosting slice in `docs/evaluation.md`. |
| Cross-run comparison and score trend | Implemented | `compare` (`internal/compare`) diffs two reports by mutant ID into four buckets -- new survivors, fixed survivors (still present, no longer actionable), removed mutants (no longer present at all, any prior verdict), and an unchanged count -- with `--format json` giving all four as clean, always-present fields for CI, plus a conservative `likely_shifted` correlation (`internal/compare.findLikelyShifts`) for the specific case where an unrelated earlier edit shifts a mutant's byte-offset-based ID. `record`/`trend` (`internal/history`) keep an NDJSON score-history log. See `docs/limitations.md` limitation 12 for exactly what `likely_shifted` does and does not claim. |
| Conservative equivalent-mutant suppression | Implemented, narrow by design | The boundary operator recognizes one exact, locally provable pattern -- a comparison dominated by an `if X != Y { return X < Y }`-shaped guard on the same two operands (`internal/frontend.detectGuardedComparison`) -- and marks it `EQUIVALENT`, skipping execution, rather than generating an ordinary mutant. Confirmed against this project's own previously-documented case (`docs/evaluation.md`, "Guarded sort comparisons"): the real `sort.Slice` comparator in `internal/frontend.Discover` is now correctly suppressed. See `docs/limitations.md` limitation 7 for exactly what this does and does not claim. |

## Clarifications

### Timeout wording

Section 13 of the functional specification was aligned with the rest of the document and the implementation: backend timeouts produce the first-class `TIMEOUT` verdict. Timeouts are never counted as kills or survivors and are excluded from the score.

### Runtime panics

A valid mutant that causes a selected test to panic is `KILLED`: the test suite distinguished the mutant. `INVALID` is reserved for source mutations that do not compile or type-check. Operational failures that cannot be classified are `UNKNOWN`.

### Configuration syntax

The dependency-free v0.1 series accepts a strict, flat subset of TOML and YAML, plus YAML's native block-list style for list-valued keys. Unsupported tables, nested maps, anchors, and multiline strings fail with a diagnostic instead of being silently approximated. This is a permanent design decision, not an open publication question — see `docs/decisions/0001-config-parser-scope.md`.

## Not yet implemented

- Distributed CI execution (M4).
- Structured `go test -json` classification for all build/test failure modes.
- Assertion or contract attribution beyond named failing tests.
- General equivalent-mutant proofs (only the one narrow boundary-operator case above is implemented; most equivalence remains undecided by design -- see `docs/limitations.md` limitation 7).
- Full TOML and YAML language support.
