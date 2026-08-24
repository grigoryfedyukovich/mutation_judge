# Mutation Judge tutorial

This tutorial starts with a deliberately weak test suite, uses a surviving mutant to identify the missing case, repairs the tests, and then moves through boolean attribution, test selection, invalid mutants, generated code, Git-diff analysis, reports, caching, CI policy, SARIF/GitHub annotations, and tracking survivors across runs.

All commands are intended to run from the repository root.

## 1. Build the tool

Mutation Judge requires Go 1.22 or newer and the local `go` command.

```bash
go version
go test ./...
mkdir -p ./bin
go build -o ./bin/mutation-judge ./cmd/mutation-judge
./bin/mutation-judge --version
```

Inspect the effective defaults:

```bash
./bin/mutation-judge --print-config
```

The executable never edits the analyzed working tree. It copies the module to a temporary sandbox, applies one mutation there, runs the selected tests, and restores the sandbox before trying the next mutation.

## 2. Read a report before trusting its score

Every result answers a concrete question:

> Would the selected `go test` command distinguish this one source change from the original program?

The verdicts are:

| Verdict | Meaning |
|---|---|
| `KILLED` | The selected tests failed under the mutant. |
| `SURVIVED` | The selected tests still passed. |
| `INVALID` | The mutated program did not compile or type-check. |
| `TIMEOUT` | The explicit deadline expired. |
| `UNKNOWN` | A backend completed without a defensible classification. |
| `UNSUPPORTED` | A backend explicitly declined the case. |

The mutation score is:

```text
killed / (killed + survived)
```

`INVALID` and `TIMEOUT` do not enter the denominator. A high score is evidence about the selected mutations and selected tests, not a universal measure of test quality. A survivor can expose a missing test, or it can be behaviorally equivalent in all reachable states.

## 3. Walkthrough: find a missing boundary test

The first example calls a callback only for positive values:

```go
// examples/boundary/counter.go
func CountPositive(n int, process func(int)) {
    if n > 0 {
        process(n)
    }
}
```

Its test covers a positive and a negative number, but not zero:

```go
// examples/boundary/counter_test.go
func TestCountPositive(t *testing.T) {
    calls := 0
    CountPositive(2, func(int) { calls++ })
    CountPositive(-1, func(int) { calls++ })
    if calls != 1 {
        t.Fatalf("calls = %d, want 1", calls)
    }
}
```

Run only boundary mutations:

```bash
./bin/mutation-judge \
  --no-cache \
  --operators boundary \
  ./examples/boundary
```

The essential report is:

```text
SURVIVED ... examples/boundary/counter.go:5:7 replace comparison > with >=
  coverage: covered
  suggested test: add a boundary case where n equals 0 and assert the original branch behavior
    - if n > 0 {
    + if n >= 0 {

summary
  1 mutants generated
  0 killed, 1 survived
  score: 0.0% excluding invalid/timeout/unknown/unsupported
```

### Why it survived

For the existing inputs, `n > 0` and `n >= 0` have the same result:

| `n` | `n > 0` | `n >= 0` |
|---:|---:|---:|
| `2` | true | true |
| `-1` | false | false |

They differ only at `n == 0`. Baseline coverage says the statement executed; it does not mean the assertion distinguished both sides of the boundary.

### Repair the test

A test that kills the mutant is:

```go
func TestZeroDoesNotProcess(t *testing.T) {
    calls := 0
    CountPositive(0, func(int) { calls++ })
    if calls != 0 {
        t.Fatalf("zero produced %d calls, want 0", calls)
    }
}
```

The repository includes the repaired version as a separate example:

```bash
./bin/mutation-judge \
  --no-cache \
  --operators boundary \
  ./examples/boundary_fixed
```

Expected result:

```text
KILLED ... replace comparison > with >=
  killed by: TestCountPositive

summary
  1 mutants generated
  1 killed, 0 survived
  score: 100.0% excluding invalid/timeout/unknown/unsupported
```

This before/after pair is the central Mutation Judge workflow:

1. Inspect a survivor's exact diff.
2. Identify an input that makes the original and mutant disagree.
3. Add an assertion for the intended behavior.
4. Re-run and confirm that the mutant is killed.

## 4. Walkthrough: attribute boolean mutants to tests

The boolean example contains a short-circuiting guard:

```go
func ShouldRetry(err error, retryable func(error) bool) bool {
    return err != nil && retryable(err)
}
```

Mutation Judge generates two deletion mutants:

```text
err != nil && retryable(err)  ->  (err != nil)
err != nil && retryable(err)  ->  (retryable(err))
```

Run the example:

```bash
./bin/mutation-judge \
  --no-cache \
  --operators boolean \
  ./examples/boolean
```

Expected result:

```text
KILLED ... delete the left operand of &&
  killed by: TestNilFailure
KILLED ... delete the right operand of &&
  killed by: TestPermanentFailure
```

The test names explain two different obligations:

- `TestNilFailure` preserves the nil guard and short-circuit behavior.
- `TestPermanentFailure` preserves the retryability predicate.

Responsible-test attribution is extracted from standard `go test` failure lines. A package-level panic, initialization failure, or unusual test harness can kill a mutant without yielding a named test.

## 5. Walkthrough: test selection changes the claim

Mutation adequacy is always relative to the selected tests. The `test_selection` example grants a discount to VIPs or coupon holders:

```go
func EligibleForDiscount(vip, hasCoupon bool) bool {
    return vip || hasCoupon
}
```

With the complete package test suite, both operand-deletion mutants are killed:

```bash
./bin/mutation-judge \
  --no-cache \
  --operators boolean \
  ./examples/test_selection
```

```text
2 mutants generated
2 killed, 0 survived
score: 100.0% excluding invalid/timeout/unknown/unsupported
```

Now select only the VIP test:

```bash
./bin/mutation-judge \
  --no-cache \
  --operators boolean \
  --test-run '^TestVIPDiscount$' \
  ./examples/test_selection
```

The mutant that keeps only `vip` survives because the coupon test was excluded:

```text
SURVIVED ... delete the right operand of ||
  suggested test: add a case where vip is false and hasCoupon is true,
                  then assert the disjunction remains true
    - return vip || hasCoupon
    + return (vip)

KILLED ... delete the left operand of ||
  killed by: TestVIPDiscount

score: 50.0% excluding invalid/timeout/unknown/unsupported
```

This is not a contradiction. The two runs ask different questions:

```text
all package tests:  can the complete selected suite distinguish the mutants?
VIP test only:      can TestVIPDiscount alone distinguish the mutants?
```

Use `--test-run` for focused diagnosis, but use a broad enough selection for the policy claim you intend to make.

## 6. Walkthrough: arithmetic mutations and invalid programs

Arithmetic mutation is opt-in because it often has a larger semantic distance and a higher invalid-mutant rate.

The arithmetic example contains numeric operations and string concatenation:

```go
func Total(subtotal, fee int) int {
    return subtotal + fee
}

func Product(value, quantity int) int {
    return value * quantity
}

func Greeting(name string) string {
    return "hello, " + name
}
```

Run it with the arithmetic operator:

```bash
./bin/mutation-judge \
  --no-cache \
  --operators arithmetic \
  ./examples/arithmetic
```

Expected classification:

```text
KILLED  replace arithmetic operator + with -   killed by TestTotal
KILLED  replace arithmetic operator * with /   killed by TestProduct
INVALID replace arithmetic operator + with -   string subtraction does not type-check

summary
  3 mutants generated
  2 killed, 0 survived, 1 invalid
  score: 100.0% excluding invalid/timeout/unknown/unsupported
```

The invalid string mutant is reported, but excluded from the score. Mutation Judge does not count compiler rejection as test evidence.

## 7. Walkthrough: generated source is opt-in

Generated Go source is skipped by default when its header contains both `Code generated` and `DO NOT EDIT`.

The generated-code example contains one ordinary source file and one generated source file. Without the opt-in flag:

```bash
./bin/mutation-judge \
  --no-cache \
  --operators boolean \
  ./examples/generated
```

Only the ordinary file contributes mutants:

```text
2 mutants generated
```

Include generated source explicitly:

```bash
./bin/mutation-judge \
  --no-cache \
  --operators boolean \
  --include-generated \
  ./examples/generated
```

Now both files contribute:

```text
4 mutants generated
```

Only enable this when the generated file is intentionally treated as maintained source. In many projects, the more useful target is the generator or its input rather than the generated output.

## 8. Walkthrough: analyze only changed lines

`--changed REV` asks Git for a zero-context diff and retains mutation spans that overlap added or modified lines in the current source tree.

In a Git checkout, make a semantics-preserving edit to the incremental example:

```diff
-return r == '_' || r >= 'a' && r <= 'z'
+return r == '_' || (r >= 'a' && r <= 'z')
```

Inspect the exact changed lines:

```bash
git diff --unified=0 HEAD -- examples/incremental/parser.go
```

Then run:

```bash
./bin/mutation-judge \
  --changed HEAD \
  --operators boundary,boolean \
  ./examples/incremental
```

For a feature branch, compare against the integration branch:

```bash
git fetch origin main
./bin/mutation-judge --changed origin/main ./...
```

Important details:

- Deleted-only lines cannot produce a current-source mutation.
- A multiline expression is selected when its mutation span overlaps a changed line.
- If no mutation site overlaps the diff, the report can contain zero mutants.
- The baseline still has to pass before any mutant is classified.
- The archive itself may not contain Git history; run this section in a clone or an initialized repository.

## 9. Use a project configuration file

Copy the example configuration:

```bash
cp mutation-judge.toml.example mutation-judge.toml
```

A practical local configuration is:

```toml
operators = ["boundary", "boolean"]
timeout = "20s"
test_run = ""
format = "text"
output = ""
cache_dir = ".mutation-judge/cache"
cache = true
max_mutants = 0
ci_min_score = 0
ci_exit_code = 10
include_generated = false
changed = ""
progress = true
```

Mutation Judge looks for these names from the current directory toward the module root:

```text
mutation-judge.toml
.mutation-judge.toml
mutation-judge.yaml
.mutation-judge.yaml
mutation-judge.yml
.mutation-judge.yml
```

CLI flags override file values:

```bash
./bin/mutation-judge \
  --operators boundary \
  --timeout 8s \
  --max-mutants 20 \
  ./pkg/...
```

Print the final merged configuration for reproducibility:

```bash
./bin/mutation-judge --timeout 8s --print-config
```

Unknown configuration keys are errors. The v0.1 parser intentionally supports only the documented flat scalar/list subset of TOML and YAML. Nested tables/maps, block lists, anchors, and multiline strings are rejected rather than guessed.

## 10. Produce JSON for scripts and coding agents

Write a versioned machine-readable report:

```bash
./bin/mutation-judge \
  --format json \
  --output ./artifacts/mutation-report.json \
  ./examples/...
```

The top-level schema identifier is:

```text
mutation-judge.report/v1
```

With `jq`, inspect the summary:

```bash
jq '.summary' ./artifacts/mutation-report.json
```

List survivors with their source locations and suggestions:

```bash
jq -r '
  .results[]
  | select(.verdict == "SURVIVED")
  | "\(.mutation.span.file):\(.mutation.span.start_line)\n" +
    "  \(.mutation.description)\n" +
    "  suggestion: \(.diagnostic.suggestion)\n" +
    "\(.mutation.diff)"
' ./artifacts/mutation-report.json
```

List responsible tests:

```bash
jq -r '
  .results[]
  | select(.verdict == "KILLED")
  | .responsible_tests[]?
' ./artifacts/mutation-report.json | sort -u
```

A consumer should reject unknown schema major versions rather than silently assuming field semantics.

## 11. Produce a self-contained HTML report

```bash
./bin/mutation-judge \
  --format html \
  --output ./artifacts/mutation-report.html \
  ./examples/...
```

The HTML file contains no external assets. It can be opened locally or uploaded as a CI artifact without granting the report network access.

## 12. Understand cache behavior

The result cache is content-addressed by the CLI version, independently versioned mutation semantics, Go/backend version, source digest, effective configuration, backend identity, and mutant identity.

Run the same command twice:

```bash
./bin/mutation-judge --operators boundary ./examples/boundary
./bin/mutation-judge --operators boundary ./examples/boundary
```

On the second run, reusable mutant results are marked:

```text
[cached]
```

The clean baseline is still executed before cached mutant verdicts are accepted. Disable cache reuse while debugging:

```bash
./bin/mutation-judge --no-cache ./pkg/...
```

Remove local cached results with:

```bash
rm -rf .mutation-judge/cache
```

## 13. Bound an exploratory run

For a first run on a larger module, use a deterministic mutant bound and a per-command timeout:

```bash
./bin/mutation-judge \
  --operators boundary,boolean \
  --max-mutants 25 \
  --timeout 15s \
  ./...
```

The frontend sorts candidates by file, source offset, and stable mutant ID before applying `--max-mutants`. The report distinguishes candidates discovered before the bound, retained after it, and actually completed. A bounded run is not a whole-project score; preserve the bound in any published result.

Serial runs emit deterministic progress lines to stderr:

```text
[12/25] M-abc123 boundary pkg/parser.go:48
```

Suppress them for quiet automation with `--progress=false`. JSON or HTML report content remains on stdout or in the selected output file; progress never contaminates that artifact.

## 14. Add an explicit CI policy

Surviving mutants do not make a normal analysis command fail. This keeps diagnostic findings separate from tool errors.

Enable a policy explicitly:

```bash
./bin/mutation-judge \
  --format json \
  --output mutation-report.json \
  --ci-min-score 80 \
  --ci-exit-code 10 \
  ./...
```

Exit-code contract:

| Exit code | Meaning |
|---:|---|
| `0` | Analysis completed and any enabled score policy passed. |
| `2` | Invalid input, configuration error, package error, or failing baseline. |
| `3` | Internal invariant failure. |
| configured code, default `10` | Analysis completed but the explicit CI score policy failed. |

A minimal GitHub Actions step is:

```yaml
- name: Run Mutation Judge
  run: |
    mkdir -p ./bin
    go build -o ./bin/mutation-judge ./cmd/mutation-judge
    ./bin/mutation-judge \
      --format json \
      --output mutation-report.json \
      --ci-min-score 80 \
      --ci-exit-code 10 \
      ./...

- name: Upload mutation report
  if: always()
  uses: actions/upload-artifact@v4
  with:
    name: mutation-report
    path: mutation-report.json
```

For an incremental pull-request job, fetch the target branch and add `--changed origin/main`. For early adoption, first archive reports without enforcing a threshold. Establish the policy only after reviewing invalid rates, equivalent survivors, runtime, and selection scope on the actual repository.

## 15. Emit SARIF or inline GitHub annotations

Two more `--format` values exist specifically for surfacing survivors where a reviewer will actually see them, without requiring `jq` against the JSON report:

```bash
./bin/mutation-judge --operators boundary --format sarif --output mutation-report.sarif ./...
```

`--format sarif` produces a SARIF 2.1.0 log. Uploading it makes each survived mutant a GitHub code scanning alert, with the file, line, and suggested test attached, and dismissible/trackable like any other code scanning finding:

```yaml
- name: Run Mutation Judge
  run: |
    mkdir -p ./bin
    go build -o ./bin/mutation-judge ./cmd/mutation-judge
    ./bin/mutation-judge --format sarif --output mutation-report.sarif ./...

- name: Upload SARIF
  if: always()
  uses: github/codeql-action/upload-sarif@v3
  with:
    sarif_file: mutation-report.sarif
```

Only `SURVIVED`, `TIMEOUT`, and `UNKNOWN` verdicts appear as SARIF results (`warning`, `note`, and `note` respectively) -- these are the three verdicts that mean "not positively confirmed as tested" (see [`docs/semantics.md`](semantics.md)). `KILLED` and `INVALID` never appear; there's nothing actionable to flag. Each result's `partialFingerprints` carries the mutant's own content-addressed ID, so GitHub can track one alert across commits instead of treating every run as entirely new findings.

For a lighter-weight integration with no separate upload step, `--format github` writes GitHub Actions workflow commands directly to stdout:

```bash
./bin/mutation-judge --operators boolean --test-run '^TestVIPDiscount$' ./examples/test_selection --format github
```

```text
::warning file=examples/test_selection/discount.go,line=5,endLine=5,col=9,title=MJ-BOOL-DROP-RIGHT M-3e1468a21c0a::mutation survived: delete the right operand of ||
suggested test: add a case where vip is false and hasCoupon is true, then assert the disjunction remains true
::notice title=mutation-judge summary::50.0% excluding invalid/timeout/unknown/unsupported (1 killed, 1 survived, 0 invalid, 0 timeout, 0 unknown, 0 unsupported, 1 flagged)
```

(The `%0A` line break and `%25` percent sign in the tool's actual raw output are GitHub's own workflow-command escaping for newlines and `%` inside a message; shown here unescaped for readability.) Run inside any workflow step and these lines become inline PR annotations immediately, no artifact upload required. A run with nothing to flag still emits the summary `::notice::` line, so a clean pass is visible in the log rather than producing silent output.

## 16. Track survivors and score across runs

Three more subcommands turn one-shot reports into a quality-tracking history: `compare` diffs two reports at the mutant level, and `record`/`trend` keep a running score log. Unlike the main command, these read `--format json` reports that already exist rather than running analysis themselves -- they're pure post-processing, so they're fast regardless of package size.

```bash
./bin/mutation-judge --operators boundary --format json --output baseline.json ./pkg
# ... fix a test ...
./bin/mutation-judge --operators boundary --format json --output current.json ./pkg

./bin/mutation-judge compare --baseline baseline.json --current current.json
```

```text
compare: baseline vs current
  baseline score: 0.0% excluding invalid/timeout/unknown/unsupported
  current score:  100.0% excluding invalid/timeout/unknown/unsupported

new survivors: 0

fixed survivors: 1
  KILLED counter.go:5:7 replace comparison > with >=

unchanged: 0
```

`new survivors` are mutants actionable (`SURVIVED`, `TIMEOUT`, or `UNKNOWN`) in `current` but not in `baseline` -- either a previously-killed mutant regressed, or a brand-new mutant from new code was actionable from the start. `fixed survivors` are the reverse. Both get per-mutant detail; `unchanged` is just a count, since that's normally the overwhelming majority. Add `--fail-on-new-survivors` (with `--fail-exit-code`, default 10) to fail a CI step specifically when a change introduces a new gap, distinct from the overall `--ci-min-score` threshold.

**Matching is by mutant ID, and that has a real limitation worth understanding before relying on it.** A mutant's ID hashes its file path and its raw byte offset in that file -- not an AST-stable identity (see `internal/frontend.mutationID`). Editing anything earlier in the same file shifts the byte offset, and so the ID, of every mutant after that point, even ones whose actual code never changed. A file the change never touches at all compares perfectly; a file the change edits will show some ID churn below the edit point that isn't a real gap or a real fix, just noise from the shift. This is why the example above patches `counter_test.go`, not `counter.go` -- keeping the production file untouched is what keeps the mutant's ID, and so its identity across the two reports, stable.

For a running score history:

```bash
./bin/mutation-judge record --label "PR #101" baseline.json
./bin/mutation-judge record --label "PR #102" current.json
./bin/mutation-judge trend
```

```text
PR #101  0.0%
PR #102  100.0%
```

`record` appends one entry per call to `.mutation-judge/history.ndjson` by default (`--history-file` to use another path); `trend` reads it back, oldest first. The label is entirely the caller's choice -- a PR number, branch name, or commit SHA are all reasonable, and Mutation Judge doesn't try to infer one itself, since which is meaningful is a property of the caller's CI, not of a report. Both subcommands also take `--format json` for scripting.

## 17. Diagnose common outcomes

### Baseline tests fail

Mutation Judge stops before executing mutants:

```text
analysis error: baseline tests must pass before mutation analysis
```

Run the reported test command directly and repair the baseline. A mutation verdict is meaningful only relative to a passing original program.

### Zero mutants are generated

Check:

- whether the selected files contain supported operators;
- whether `--changed` excluded the mutation lines;
- whether files are generated and excluded by default;
- whether package patterns select the expected packages;
- whether `--max-mutants` was set unexpectedly;
- the effective configuration from `--print-config`.

A zero-mutant report has no scoreable denominator and reports `n/a`.

### A covered mutant survives

Coverage only means a baseline statement on the mutated line executed. Inspect the input values and assertions. The boundary tutorial is exactly this case.

### A mutant is invalid

Read the changed operator and compiler output in JSON evidence. Invalid mutants are useful diagnostics about operator applicability, but not proof of test adequacy.

### A mutant times out

Increase the explicit timeout only after deciding that the test is expected to complete. `TIMEOUT` is not converted to a kill and is excluded from the score.

### A killed mutant has no responsible test

The process may have failed before Go printed a `--- FAIL:` line, for example during package initialization or from a process-wide crash. Inspect `diagnostic.evidence.backend_output` in JSON.

## 18. Apply Mutation Judge to a real package

A practical sequence is:

```bash
# 1. Confirm the ordinary baseline.
go test ./pkg/...

# 2. Start with low-distance operators and a bound.
./bin/mutation-judge \
  --no-cache \
  --operators boundary,boolean \
  --max-mutants 25 \
  ./pkg/...

# 3. Review survivors, not only the score.
./bin/mutation-judge \
  --format json \
  --output ./artifacts/pkg-mutations.json \
  --operators boundary,boolean \
  ./pkg/...

# 4. Add focused tests for defensible survivors and rerun.

# 5. Narrow future pull-request runs to changed lines.
./bin/mutation-judge --changed origin/main ./pkg/...
```

For every survivor, record one of three conclusions:

1. **Missing test:** add a concrete distinguishing input and assertion.
2. **Equivalent or unreachable:** document the guard or invariant that makes the mutation irrelevant.
3. **Operator not useful here:** exclude the operator from the policy scope rather than inflating the score with noise.

## 19. Run all packaged examples

The example runner builds a temporary executable and exercises all examples that do not require a Git diff:

```bash
./examples/run-all.sh
```

See [examples/README.md](../examples/README.md) for the example matrix and focused commands.
