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
    @@ -5,1 +5,1 @@
    -    if n > 0 {
    +    if n >= 0 {
```

The [full tutorial](docs/tutorial.md) follows this survivor through test repair, then covers selected-test scope, arithmetic invalids, generated source, Git-diff mode, JSON/HTML reports, caching, and CI policy.

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
| `--operators` | `boundary`, `boolean` on by default; `arithmetic`, `errorreturn`, `switch`, `loop`, `channel` are opt-in |
| `--changed REV` | Generate mutants only on added or modified lines from `git diff REV` |
| `--test-run REGEXP` | Pass a test selection regexp to `go test -run` |
| `--timeout DURATION` | Per-baseline and per-mutant command deadline |
| `--max-mutants N` | Deterministic execution bound; `0` is unlimited |
| `--format` | `text`, `json`, or `html` |
| `--output PATH` | Write the report to a file |
| `--no-cache` | Disable content-addressed result reuse |
| `--ci-min-score PCT` | Return the configured CI failure code below this score |
| `--ci-exit-code CODE` | CI policy failure code, default `10` |
| `--progress=false` | Suppress per-mutant progress lines on stderr |
| `--narrow-test-scope` | Run each mutant only against tests that can observe it, computed from the module's dependency graph, instead of the full pattern set every time; opt-in, see `docs/performance.md` |
| `--workers N` | Run N mutants concurrently, each in its own sandbox; default 1 (sequential, unchanged from earlier versions), see `docs/performance.md` |

A successful analysis returns `0` regardless of survivors. Invalid input and baseline failures return `2`; internal failures return `3`; an enabled CI score policy uses its configured code.

## Supported mutations

### Boundary

- `<` → `<=`
- `<=` → `<`
- `>` → `>=`
- `>=` → `>`

### Boolean

- `a && b` → `(a)` and `(b)`
- `a || b` → `(a)` and `(b)`
- `!a` → `(a)`
- `true` ↔ `false`

### Arithmetic, opt-in

- `+` ↔ `-`
- `*` ↔ `/`

Arithmetic mutants can be compile-invalid or panic at runtime. Compile/type failures are `INVALID`; a runtime panic observed by the selected tests is `KILLED` because the tests distinguished the valid mutant.

## Verdicts and score

- **KILLED:** selected tests fail under the mutant. Named failing tests are extracted when Go reports them.
- **SURVIVED:** selected tests pass under the mutant.
- **INVALID:** the mutant does not compile or type-check.
- **TIMEOUT:** the explicit command deadline expired.
- **UNKNOWN / UNSUPPORTED:** reserved first-class report values for future backends.

The score is `killed / (killed + survived)`. Invalid and timeout mutants are excluded. The report always prints the configured mutant and timeout bounds.

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
3. Copy the module to a temporary sandbox; the working tree is never mutated.
4. Run a clean baseline `go test -count=1` with coverage. Analysis stops if the baseline does not pass.
5. Apply exactly one mutant with an atomic same-directory replacement, run the same selected tests (or, with `--narrow-test-scope`, only the subset of tests that can actually observe that mutant's package — see `docs/performance.md`), classify the result, and atomically restore the original file and mode.
6. Render diagnostics and cache compatible backend results.

Cache keys include the tool version, independently versioned operator semantics, all Go/test source content, module files, effective configuration, backend name/version, and mutant identity. Cache entries use a versioned schema.

## Incremental workflow

```bash
git switch feature/parser-cleanup
mutation-judge --changed origin/main ./...
```

Only mutation spans overlapping lines added or modified in the zero-context Git diff are considered. Deleted-only hunks do not generate a source mutation because no current source span exists.

## JSON contract

JSON output uses schema `mutation-judge.report/v1`. Each result contains:

- a stable mutant and verdict rule ID;
- exact source span and unified diff;
- verdict and responsible tests;
- baseline coverage status when available;
- evidence, assumptions, and a mechanically grounded suggestion for survivors;
- tool/backend versions, effective configuration, explicit bounds, and phase timing.

Consumers should reject unknown schema major versions.

## Development

```bash
gofmt -w .
go vet ./...
go test ./...
go build ./cmd/mutation-judge
```

The integration test runs the boundary example through the actual CLI. Unit tests cover mutation discovery, strict configuration, Git diff parsing, result rendering, generated-source selection, mechanically distinguishing boolean suggestions, and the backend boundary through a fake runner. The packaged examples can be exercised with `./examples/run-all.sh`.

## Honest limitations

The v0.1 implementation mutates ordinary production Go files returned by `go list`; it does not mutate tests, generated files by default, assembly, templates, or code generated during tests. It runs package patterns as one selected test command and infers responsible tests from standard `go test` failure lines. Coverage is baseline statement coverage, not mutant-specific dynamic slicing. See [docs/limitations.md](docs/limitations.md).

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
