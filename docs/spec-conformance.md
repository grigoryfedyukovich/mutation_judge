# Specification conformance

This document maps the repository to [the functional specification](../SPECIFICATION.md). It is intentionally explicit about implementation choices and remaining work.

## Implemented vertical slice

| Specification area | Status | Evidence |
|---|---|---|
| Curated Go AST mutations | Implemented | Boundary, boolean deletion/negation/literal, and opt-in arithmetic operators in `internal/frontend`. |
| One mutant at a time | Implemented | Atomic apply/run/restore in one isolated temporary module copy. |
| Test classification | Implemented | `KILLED`, `SURVIVED`, `INVALID`, `TIMEOUT`, `UNKNOWN`, and `UNSUPPORTED` model values. |
| Responsible tests | Implemented with documented limits | Standard `--- FAIL:` events are extracted and sorted. |
| Exact survivor diff and suggestion | Implemented | Every surviving result carries a unified diff and operator-specific scenario. |
| Git diff mode | Implemented | Zero-context changed-line mapping with deleted-file and zero-count handling. |
| Coverage explanation | Implemented | Baseline statement coverage annotates each mutation span. |
| Text, JSON, HTML | Implemented | JSON schema `mutation-judge.report/v1`; embedded maintainable HTML template. |
| Strict project configuration | Implemented as a documented subset | Flat scalar/list TOML or YAML syntax, strict unknown-key rejection, CLI overrides, `--print-config`. |
| Content-addressed cache | Implemented | Tool version, operator-semantics version, source digest, effective config, backend identity/version, and mutant identity. |
| Deterministic bounds | Implemented | Reports distinguish candidates discovered before and retained after `max_mutants`. |
| Backend abstraction | Implemented | Narrow `runner.Backend`, optional backend name/version descriptor, deterministic fakes in tests. |
| Local/privacy boundary | Implemented | No shell interpolation or source upload; normal execution requires no network. |
| CI policy exit | Implemented | Successful analysis returns zero unless an explicit score policy fails. |
| Three running examples | Exceeded | Seven examples plus `examples/run-all.sh`. |
| Real-world evaluation | Implemented, bounded | Self-hosting slice in `docs/evaluation.md`. |

## Clarifications

### Timeout wording

Section 13 of the functional specification was aligned with the rest of the document and the implementation: backend timeouts produce the first-class `TIMEOUT` verdict. Timeouts are never counted as kills or survivors and are excluded from the score.

### Runtime panics

A valid mutant that causes a selected test to panic is `KILLED`: the test suite distinguished the mutant. `INVALID` is reserved for source mutations that do not compile or type-check. Operational failures that cannot be classified are `UNKNOWN`.

### Configuration syntax

The dependency-free v0.1 series accepts a strict, flat subset of TOML and YAML, plus YAML's native block-list style for list-valued keys. Unsupported tables, nested maps, anchors, and multiline strings fail with a diagnostic instead of being silently approximated. This is a permanent design decision, not an open publication question — see `docs/decisions/0001-config-parser-scope.md`.

## Not yet implemented

- Distributed CI execution (M4).
- Parallel isolated workers.
- Structured `go test -json` classification for all build/test failure modes.
- Assertion or contract attribution beyond named failing tests.
- Equivalent-mutant proofs.
- Full TOML and YAML language support.
