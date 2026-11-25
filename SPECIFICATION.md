# Mutation Judge — Functional Specification

## 1. Purpose

Generate small semantics-oriented source mutations and explain which tests, assertions, or contracts kill each mutation.

## 2. Product goals

- Implement a curated set of Go AST mutations.
- Run selected tests and classify mutants.
- Explain surviving mutants by changed behavior and coverage.
- Support incremental mutation on Git diffs.

## 3. Explicit non-goals

- Generating thousands of arbitrary textual mutations.
- Replacing code review.
- Treating mutation score as a universal quality metric.

## 4. Primary users

- Developers reviewing or validating small but semantically meaningful changes.
- Maintainers who need reproducible diagnostics rather than opaque scores.
- Researchers and students who want an executable, bounded implementation of a classical analysis idea.
- CI systems and coding agents consuming deterministic JSON output.

## 5. Inputs

Go packages, test selection, mutation operators, and optional changed lines.

## 6. Outputs and verdicts

Emit a per-mutant verdict and supporting evidence in text, JSON, or HTML.
SARIF and GitHub annotations expose actionable findings. JSON reports use
`mutation-judge.report/v1`. See the [verdict definitions](docs/semantics.md#verdict-interpretation).

## 7. Command-line interface

```bash
mutation-judge ./...
mutation-judge --changed origin/main ./...
mutation-judge --operators boundary,boolean ./pkg/...
```

The CLI must return exit code `0` for a successful analysis run even when it finds a user-level defect, and a separate configurable nonzero CI exit code when policy requires failure. Invalid input and internal errors always use distinct exit codes.

## 8. Functional requirements

1. Each test sandbox contains one active mutant. Parallel workers use independent sandboxes.
2. Invalid mutants are excluded from score.
3. Timeout policy is explicit.
4. Each surviving mutant includes an exact source diff and suggested missing test scenario.

## 9. Architecture

- Go AST mutator.
- Build/test sandbox.
- Result cache keyed by source and test command.
- Coverage mapper.
- Report and mutant-diff renderer.

### Internal API boundary

The frontend lowers Go AST mutations to a language-neutral model of source
spans and replacements. Analysis and reporting consume that model. The test
runner exposes a narrow interface so unit tests can inject a deterministic
backend.

### Persistence and caching

The MVP may operate without a database. Cached artifacts must be content-addressed by tool version, input digest, configuration digest, and backend mode. Stale cache entries must never be accepted across incompatible semantic versions.

## 10. Semantics and trust model

- The tool must state exactly what is modeled and what is abstracted.
- Any bounded result must print its bound.
- `UNKNOWN` and `UNSUPPORTED` are first-class outcomes.
- Reports should include tool version and relevant backend version.

## 11. Running examples

The [packaged examples](examples/README.md) include a boundary survivor
and test repair, boolean operand deletion attributed to a named test, and
compile-invalid arithmetic mutants. The [tutorial](docs/tutorial.md)
walks through those examples and incremental runs on Git changes.

## 12. Configuration

Accept a strict, flat subset of project-local YAML and TOML, including YAML
block lists for list-valued keys. Reject unknown keys and unsupported syntax.
CLI flags override the file. `--print-config` emits the effective settings.
See the [parser decision](docs/decisions/0001-config-parser-scope.md).

## 13. Error handling

- Syntax and type errors include source spans and recovery hints.
- Backend timeouts produce `TIMEOUT`, not success, failure, or `UNKNOWN`.
- Internal invariant violations produce a crash report containing a reproducible command but no source-code upload.
- Partial results may be emitted only when labeled incomplete.

## 14. Performance targets

These are design targets. See [performance](docs/performance.md) for measured results and current resource limits.

- Startup under one second for small examples after installation.
- Typical examples complete in under five seconds.
- Memory use remains under 512 MB for documented MVP limits.
- Provide configurable timeouts and a limit on retained mutants.
- Report time spent in discovery, baseline, mutant execution, and rendering.

## 15. Security and privacy

- No network access is required for normal analysis.
- Input source remains local unless the user explicitly enables an integration.
- Generated reports avoid embedding unrelated source text.
- Subprocess execution, when required, uses argument arrays rather than shell interpolation.

## 16. Milestones

- M1: AST mutations and isolated test runs.
- M2: caching and HTML report.
- M3: Git diff mode and coverage.
- M4: distributed CI execution.

## 17. Definition of done

The project is portfolio-ready when it has one polished end-to-end workflow, at least three running examples, a clearly documented semantic boundary, reproducible tests, one real-world evaluation, and an issue list that separates correctness work from optional feature expansion.

## Diagnostic contract

Every diagnostic should contain:

1. A stable rule or verdict identifier.
2. Source location or input element.
3. A concise statement of the issue.
4. Evidence such as a source diff, test result, or coverage observation.
5. Assumptions, bounds, and approximation mode.
6. A suggested action only when it is grounded and mechanically defensible.

## Testing strategy

- **Unit tests:** mutation discovery, workspace handling, verdict classification, and rendering.
- **Golden tests:** full input-to-output examples committed as fixtures.
- **Differential tests:** compare alternative discovery, execution, and rendering paths for equivalent results.
- **Regression tests:** every reported bug receives a minimized fixture.
- **Corpus tests:** run against real Go repositories and review survivors, invalid mutants, and runtime.

## Release criteria for v0.1

- The supported input subset is documented precisely.
- All examples in this specification run in CI.
- Machine-readable output is versioned.
- Unsupported or unknown cases never appear as successful proofs.
- Linux and macOS builds pass.
- The README includes a two-minute demo and a technically honest limitations section.

## Implementation status

| Area | Implementation |
|---|---|
| Discovery and bounds | The [operator catalog](docs/semantics.md#mutation-operators) defines mutation shapes. Reports record candidates discovered, retained, and completed. Incremental mode uses a captured Git revision, index, and source snapshot. |
| Execution and evidence | An uninstrumented baseline must pass. Separate statement coverage annotates mutation spans. Tests and assertion log lines are attributed when the Go event stream supplies them. A runtime panic can kill a valid mutant, and a compilation failure makes it invalid. |
| Reproducibility | The [architecture](docs/architecture.md) documents the narrow backend interface, disposable test trees, and content-addressed result cache. |
| Reports across runs | The [comparison walkthrough](docs/tutorial.md#16-track-survivors-and-score-across-runs) covers compatibility checks, exploratory diffs, mutant ID matching, and NDJSON history. |
| Validation | The [local qualification command](README.md#development) builds and runs the test suite and examples. The [evaluation](docs/evaluation.md) records bounded self-hosted and external corpus runs. |

[Limitations](docs/limitations.md) describe the supported input and evidence
boundaries. [Open work](ISSUES.md) includes distributed CI execution. Full
configuration language support and general equivalence proofs are outside
the implemented scope.
