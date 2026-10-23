# Mutation Judge

Mutation Judge is a small, deterministic mutation-adequacy analyzer for Go. It generates a curated set of semantic source mutations, runs the selected tests with one mutant active at a time, and explains which mutants were killed or survived.

It is intentionally not a high-volume mutation fuzzer. The goal is a readable report that helps answer: **which small semantic mistakes can the current tests detect?**

## Two-minute demo

```bash
go test ./...
mkdir -p ./bin
go build -o ./bin/mutation-judge ./cmd/mutation-judge

# A boundary mutant survives because the example omits n == 0.
./bin/mutation-judge --operators boundary ./examples/boundary

# The same production code kills the mutant after adding the zero case.
./bin/mutation-judge --operators boundary ./examples/boundary_fixed

# Boolean operand deletion is killed by named tests.
./bin/mutation-judge --operators boolean ./examples/boolean

# Arithmetic mutations include a compile-invalid string mutant.
./bin/mutation-judge --operators arithmetic ./examples/arithmetic
```

Run the complete packaged demonstration:

```bash
./examples/run-all.sh
```

A surviving boundary mutation is reported with an exact patch and a grounded test suggestion:

```text
SURVIVED M-... examples/boundary/counter.go:5:7 replace comparison > with >=
  coverage: covered
  suggested test: add a boundary case where n equals 0 and assert the original branch behavior
    --- a/examples/boundary/counter.go
    +++ b/examples/boundary/counter.go
    @@ -2,7 +2,7 @@

     // CountPositive records calls only for positive values.
     func CountPositive(n int, process func(int)) {
    -    if n > 0 {
    +    if n >= 0 {
             process(n)
         }
     }
```

The [full tutorial](docs/tutorial.md) follows this survivor through test repair, then covers selected-test scope, arithmetic invalids, generated source, Git-diff mode, JSON/HTML/SARIF/GitHub-annotations reports, caching, CI policy, and tracking survivors across runs.

## Installation

From a local checkout:

```bash
go test ./...
mkdir -p ./bin
go build -o ./bin/mutation-judge ./cmd/mutation-judge
./bin/mutation-judge --version
```

To install from a published fork, replace `<module-path>` with its actual Go module path:

```bash
go install <module-path>/cmd/mutation-judge@latest
```

Mutation Judge requires Go 1.22 or newer and invokes the local `go` command. Normal analysis requires no network access.

## Commands

```bash
mutation-judge ./...
mutation-judge --changed origin/main ./...
mutation-judge --operators boundary,boolean ./pkg/...
mutation-judge --test-run 'TestParser|TestLexer' ./parser
mutation-judge --timeout 10s --max-mutants 30 ./...
mutation-judge --progress=false ./...
mutation-judge --print-config
```

Important flags:

| Flag | Meaning |
|---|---|
| `--operators` | `boundary`, `boolean` on by default; `arithmetic`, `errorreturn`, `switch`, `loop`, `channel`, `assignment`, `relational`, `literal`, `statement`, `condition`, `returnvalue`, `connective`, `bounds` are opt-in |
| `--changed REV` | Generate mutants on added/modified tracked lines **and all lines of non-ignored untracked Go files**; malformed Git output is an error, not an empty selection |
| `--test-run REGEXP` | Pass a test selection regexp to `go test -run` |
| `--timeout DURATION` | Per-baseline and per-mutant command deadline |
| `--preparation-timeout DURATION` | Wall-clock budget for toolchain detection and input preparation; default `2m`, `0` disables |
| `--total-timeout DURATION` | Optional wall-clock budget for the entire analysis; default `0` (off). An expired budget fails and a partial report is emitted if execution had begun |
| `--max-mutants N` | Deterministic execution bound; `0` is unlimited |
| `--format` | `text`, `json`, `html`, `sarif` (GitHub code scanning), or `github` (inline PR annotations via workflow commands, no upload step) |
| `--output PATH` | Write the report to a file |
| `--no-cache` | Disable content-addressed result reuse |
| `--ci-min-score PCT` | Return the configured CI failure code below this score |
| `--ci-exit-code CODE` | CI policy failure code, default `10` |
| `--progress=false` | Suppress per-mutant progress lines on stderr |
| `--narrow-test-scope` | Run each mutant only against tests that can observe it, computed from the module's dependency graph, instead of the full pattern set every time; opt-in, see `docs/performance.md` |
| `--coverage-test-selection` | Select ordinary tests whose baseline coverage reaches the mutation, while retaining all runnable examples and fuzz seeds in observing packages; composes with `--narrow-test-scope`. Opt-in and expensive (profiles top-level ordinary tests individually); see `docs/performance.md` |
| `--workers N` | Run up to N mutants concurrently; every mutant gets a fresh sandbox copied from the same input snapshot; default 1, see `docs/performance.md` |

A successful analysis returns `0` regardless of survivors. Invalid input and baseline failures return `2`; internal failures return `3`; an enabled CI score policy uses its configured code.

### Subcommands: tracking survivors across runs

`compare`, `record`, and `trend` are subcommands, not flags -- they must come first (`mutation-judge compare ...`, not `mutation-judge ... compare`). Unlike the default command, they read `--format json` reports that already exist rather than running analysis themselves.

```bash
mutation-judge compare --baseline old.json --current new.json [--fail-on-new-survivors]
mutation-judge record --label "PR #104" report.json
mutation-judge trend
```

`compare` compares **compatible, complete mutation experiments**. It refuses incomplete, bounded, incremental, or mismatched scopes, test settings, tool semantics, and actual per-mutant commands (exit code 2). `--allow-incompatible` produces an explicitly **exploratory** text/JSON/HTML diff: `not_analyzed` lists IDs missing from a report without evidence of removal/introduction, and `unverified_changes` lists transitions that cannot prove a test repair or regression. A CI gate (`--fail-on-new-survivors`) is not available in exploratory mode. For comparable reports, the original six verdict buckets and conservative `likely_shifted` correlation remain. `record`/`trend` keep an NDJSON score history at `.mutation-judge/history.ndjson` including completeness, bounds, operator semantics, selected test scope, and all verdict counts. See `docs/tutorial.md` §16 and `docs/limitations.md` for ID and compatibility limitations.

## Supported mutations

### Boundary

- `<` → `<=`
- `<=` → `<`
- `>` → `>=`
- `>=` → `>`

A comparison dominated by an exact `if X != Y { return X < Y }`-shaped guard on the same two operands is provably equivalent under this mutation and is classified `EQUIVALENT` instead -- never executed. The boolean operator likewise classifies a `v := true`/`false` initializer as `EQUIVALENT` when the very next statement overwrites `v` without reading it (a plain assignment, or an exhaustive if/else whose condition never mentions `v`). This is deliberately narrow; see `docs/semantics.md`, "Conservative equivalent-mutant suppression."

### Boolean

- `a && b` → `(a)` and `(b)`
- `a || b` → `(a)` and `(b)`
- `!a` → `(a)`
- `true` ↔ `false`

### Arithmetic, opt-in

- `+` ↔ `-`
- `*` ↔ `/`

Arithmetic mutants can be compile-invalid or panic at runtime. Compile/type failures are `INVALID`; a runtime panic observed by the selected tests is `KILLED` because the tests distinguished the valid mutant.

### Error-return, opt-in

- `if X != nil { ...return ...X }` → the returned `X` is replaced with `nil`, silently swallowing it.

### Switch, opt-in

- An entire `case` clause (label and body together), including `default`, is deleted from a `switch` or type-switch statement.

### Loop, opt-in

- A conditional `for`'s condition is replaced with `false`.
- An unconditional `for {}` or a `range` loop has its first body statement replaced with `break`.

Deliberately excluded: any mutation that could make a loop run forever (e.g. forcing a condition to `true`) -- it would produce a slow `TIMEOUT` on every occurrence instead of a fast `KILLED`/`SURVIVED`.

### Channel, opt-in

- A `make(chan T, N)` capacity expression is replaced with `0` (buffered becomes unbuffered).
- An entire `select` `case` clause (comm statement and body together), including `default`, is deleted.

Deliberately excluded: deleting a `close(ch)` call, for the same slow-`TIMEOUT` reason as the loop operator's exclusion above.

## Verdicts and score

- **KILLED:** selected tests fail under the mutant. Named failing tests are extracted when Go reports them.
- **SURVIVED:** selected tests pass under the mutant.
- **INVALID:** the mutant does not compile or type-check.
- **TIMEOUT:** the explicit command deadline expired.
- **UNKNOWN / UNSUPPORTED:** reserved first-class report values for future backends.
- **EQUIVALENT:** discovery itself proved the mutant behaviorally identical to the original before any test ran; never executed. Currently only three narrow cases (the last for the `returnvalue` operator: a result discarded at every call site of an unexported function): the boundary operator's guarded comparison and the boolean operator's immediately overwritten initializer (see "Supported mutations" above and `docs/limitations.md` limitation 7).

The score is `killed / (killed + survived)`. Invalid, timeout, and equivalent mutants are excluded. The report always prints the configured mutant and timeout bounds.

## Configuration

Mutation Judge discovers the first of these files in the current directory:

- `mutation-judge.toml`
- `.mutation-judge.toml`
- `mutation-judge.yaml`
- `.mutation-judge.yaml`
- `mutation-judge.yml`
- `.mutation-judge.yml`

Copy `mutation-judge.toml.example` to start. CLI flags override file values. Unknown keys are errors by default. The dependency-free v0.1 parser accepts the documented flat scalar/list subset of TOML and YAML; nested tables/maps, block lists, anchors, and multiline strings are rejected rather than guessed.

```toml
operators = ["boundary", "boolean"]
timeout = "20s"
test_run = ""
format = "text"
cache_dir = ".mutation-judge/cache"
cache = true
max_mutants = 0
ci_min_score = 0
ci_exit_code = 10
include_generated = false
changed = ""
progress = true
```

`--print-config` prints the complete effective configuration after overrides.

## How execution works

1. Resolve package patterns with `go list`.
2. Parse production `.go` files and lower mutation candidates to language-neutral source spans plus replacements.
3. Copy the module into a single stable snapshot; the working tree is never mutated.
4. Run a clean baseline `go test -count=1` with coverage. Analysis stops if the baseline does not pass.
5. For each mutant, create a fresh sandbox from the snapshot, verify the original source span, apply one atomic replacement, run the selected tests (or a narrower scope if configured), classify the result, and discard the sandbox. The baseline and profiling runs are also isolated.
6. Render diagnostics and cache compatible backend results.

Cache keys include the tool version, independently versioned operator semantics, all Go/test source content, module files, effective configuration, backend name/version, and mutant identity. They also bind the **module-relative execution directory**, actual per-mutant test patterns, `-run` selector, and resolved test package universe (so a relative `./...` in two different directories cannot reuse a stale verdict). Cache entries also account for sandbox entry types, executable/file permissions, and a SHA-256 digest of the frozen effective test environment (environment values are not written to reports). Any main-module local `replace` dependency or `GOFLAGS` with `-modfile`, `-overlay`, or `-toolexec` disables caching with a report warning, because inputs outside the snapshot cannot be fingerprinted reliably. Cache entries use a versioned schema, and older unsafe execution keys are not reused. Reports record the execution working directory and effective command with each executed or cached mutant.

### Race-enabled Go tests and coverage

Baseline coverage and per-test coverage profiling use `-covermode=atomic`.
This is required when `GOFLAGS=-race` is enabled (including when the setting
comes from `go env -w GOFLAGS=...`). The explicit mode also overrides an
incompatible `-covermode=count` inherited through `GOFLAGS`, while normal
(non-coverage) mutant executions retain the project’s configured Go flags.
Atomic counters may add profiling overhead compared with count mode, but
avoid rejecting an otherwise valid race-enabled test configuration.

## Incremental workflow

```bash
git switch feature/parser-cleanup
mutation-judge --changed origin/main ./...
```

Mutation spans on added or modified tracked lines in the zero-context Git diff are considered. Non-ignored, untracked `.go` files are treated as wholly changed (even before `git add`). Git-ignored files and files outside the selected Go package patterns are excluded. Deleted-only hunks do not generate a source mutation because no current source span exists.

## JSON contract

JSON output uses schema `mutation-judge.report/v1`. Each result contains:

- a stable mutant and verdict rule ID;
- exact source span and Git-applicable contextual unified diff (including EOF newline markers where required);
- verdict, responsible tests, and responsible assertions (testing.T log lines from failed tests);
- bounded backend diagnostic output and an `output_truncated` flag when evidence was clipped;
- baseline coverage status when available;
- evidence, assumptions, and a mechanically grounded suggestion for survivors;
- tool/backend versions, effective configuration, explicit bounds, and phase timing.

Imported reports used by `compare` and `record` must have the exact
`mutation-judge.report/v1` schema identifier and complete required structure.
The loader rejects malformed fields, duplicate mutant IDs, invalid verdicts,
summary/score inconsistencies, impossible execution bounds, and trailing JSON.
An intentionally interrupted report (`complete=false`) or one limited by
`--max-mutants` is valid input, but report comparison still treats it as
incompatible unless explicitly run in exploratory mode. Unknown fields in an
otherwise valid v1 report are tolerated.

## Development

```bash
gofmt -w .
go vet ./...
go test ./...
go build ./cmd/mutation-judge
```

The integration test runs the boundary example through the actual CLI. Unit tests cover mutation discovery, strict configuration, Git diff parsing, result rendering, generated-source selection, mechanically distinguishing boolean suggestions, and the backend boundary through a fake runner. The packaged examples can be exercised with `./examples/run-all.sh`.

## Honest limitations

The v0.1 implementation mutates ordinary production Go files returned by `go list`; it does not mutate tests, generated files by default, assembly, templates, or code generated during tests. It runs package patterns as one selected test command and infers responsible tests and assertion log text from standard `go test` failure output. Coverage is baseline statement coverage, not mutant-specific dynamic slicing. See [docs/limitations.md](docs/limitations.md).

## Documentation

- [Semantic model and trust boundary](docs/semantics.md)
- [Architecture](docs/architecture.md)
- [Limitations and next work](docs/limitations.md)
- [Step-by-step tutorial](docs/tutorial.md)
- [Runnable example matrix](examples/README.md)
- [Bounded self-hosting evaluation](docs/evaluation.md)
- [Functional specification](SPECIFICATION.md)
- [Specification conformance](docs/spec-conformance.md)
- [2026-07-18 review resolution](docs/reviews/review-resolution-v0.1.2.md)
- [Correctness and expansion issue list](ISSUES.md)

**Subprocess timeouts:** On Linux/macOS/BSD, canceled or timed-out `go test`
process groups and their ordinary descendants are terminated. On Windows,
subprocesses are suspended until assigned to a kill-on-close Job Object,
which is terminated on cancellation or closed after execution. Inherited
output pipes have a 250ms cleanup grace. Preparation commands honor analysis
cancellation. Preparation additionally has a 2-minute deadline (configurable
with `--preparation-timeout`). Every preparation subprocess has a 32 MiB
stdout/combined-output capture budget; stderr captured separately is limited
to 64 KiB. Overflow is an explicit error, never silently parsed as truncated
Go JSON, a Git diff, or a test list. Snapshot copy/verification observe
cancellation and use fixed-size file buffers. An optional `--total-timeout`
bounds the analysis phase, not final report serialization. Deliberately
detached processes remain outside the Unix descendant guarantee. These limits
do not provide a total-RSS bound (results still grow with the mutant count).
