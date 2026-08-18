# Mutation Judge — Functional Specification

**Primary language:** Go  
**Category:** Targeted mutation adequacy analyzer  
**Suggested repository name:** `mutation-judge`

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

Killed/survived/invalid/timeout mutants, responsible tests, and JSON/HTML.

## 7. Command-line interface

```bash
mutation-judge ./...
mutation-judge --changed origin/main ./...
mutation-judge --operators boundary,boolean ./pkg/...
```

The CLI must return exit code `0` for a successful analysis run even when it finds a user-level defect, and a separate configurable nonzero CI exit code when policy requires failure. Invalid input and internal errors always use distinct exit codes.

## 8. Functional requirements

1. Mutants are applied one at a time.
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

The parser/frontend must produce a language-neutral internal model. Analysis code must not depend directly on source-parser node identities except through source-span metadata. Solver, graph, or execution backends should be hidden behind a narrow trait/interface so that tests can use a deterministic fake backend.

### Persistence and caching

The MVP may operate without a database. Cached artifacts must be content-addressed by tool version, input digest, configuration digest, and backend mode. Stale cache entries must never be accepted across incompatible semantic versions.

## 10. Semantics and trust model

- The tool must state exactly what is modeled and what is abstracted.
- Any bounded result must print its bound.
- Any solver result used as evidence should be replayed or independently validated when practical.
- `UNKNOWN` and `UNSUPPORTED` are first-class outcomes.
- Reports should include tool version and relevant backend version.

## 11. Running examples

### Example 1: Boundary mutation

**Input**

```text
if n > 0 { process(n) }
```

**Run and expected output**

```text
mutant: n >= 0
SURVIVED
suggested test: n=0 should not call process
```

### Example 2: Boolean deletion

**Input**

```text
if err != nil && retryable(err)
```

**Run and expected output**

```text
mutant: if err != nil
KILLED by TestPermanentFailure
```

### Example 3: Incremental run

**Input**

```text
changed files: parser.go
```

**Run and expected output**

```text
12 mutants generated
9 killed, 2 survived, 1 invalid
score: 81.8% excluding invalid
```

## 12. Configuration

Configuration should be accepted from a project-local YAML/TOML file and overridden by CLI flags. Unknown configuration keys are errors by default. Every effective configuration can be printed with `--print-config` for reproducibility.

## 13. Error handling

- Syntax and type errors include source spans and recovery hints.
- Backend timeouts produce `TIMEOUT`, not success, failure, or `UNKNOWN`.
- Internal invariant violations produce a crash report containing a reproducible command but no source-code upload.
- Partial results may be emitted only when labeled incomplete.

## 14. Performance targets

- Startup under one second for small examples after installation.
- Typical examples complete in under five seconds.
- Memory use remains under 512 MB for documented MVP limits.
- A timeout and state/formula-size limit are configurable.
- Performance reports distinguish parsing, analysis, backend, witness extraction, and rendering time.

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


## Repository shape

```text
mutation-judge/
├── README.md
├── LICENSE
├── CHANGELOG.md
├── docs/
│   ├── semantics.md
│   ├── architecture.md
│   └── limitations.md
├── examples/
├── src/ or modules/
├── tests/
│   ├── unit/
│   ├── golden/
│   └── integration/
└── .github/workflows/
```

The initial repository should stay intentionally small. A complete vertical slice with honest limits is preferable to broad syntax support with uncertain semantics.

## Diagnostic contract

Every diagnostic should contain:

1. A stable rule or verdict identifier.
2. Source location or input element.
3. A concise statement of the issue.
4. Evidence: model, path, graph edge, conflicting rows, or other witness.
5. Assumptions, bounds, and approximation mode.
6. A suggested action only when it is grounded and mechanically defensible.

## Testing strategy

- **Unit tests:** parser, lattice/graph/constraint primitives, and rendering.
- **Golden tests:** full input-to-output examples committed as fixtures.
- **Property tests:** algebraic laws, witness replay, and graph/automata invariants where applicable.
- **Differential tests:** compare against concrete execution, compilation, or a simpler trusted algorithm.
- **Regression tests:** every reported bug receives a minimized fixture.
- **Corpus tests:** run against several real repositories or policy sets and record precision/runtime observations.

## Release criteria for v0.1

- The supported input subset is documented precisely.
- All examples in this specification run in CI.
- Machine-readable output is versioned.
- Unsupported or unknown cases never appear as successful proofs.
- Linux and macOS builds pass.
- The README includes a two-minute demo and a technically honest limitations section.
