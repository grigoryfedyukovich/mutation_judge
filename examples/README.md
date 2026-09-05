# Running examples

Build once from the repository root:

```bash
mkdir -p ./bin
go build -o ./bin/mutation-judge ./cmd/mutation-judge
```

Or execute all non-Git examples:

```bash
./examples/run-all.sh
```

## Example matrix

| Directory | Demonstrates | Command | Expected essential result |
|---|---|---|---|
| `boundary` | A covered boundary mutant surviving because zero is omitted | `./bin/mutation-judge --no-cache --operators boundary ./examples/boundary` | 1 survived |
| `boundary_fixed` | The same mutant killed after adding the exact boundary case | `./bin/mutation-judge --no-cache --operators boundary ./examples/boundary_fixed` | 1 killed |
| `equivalent` | A guarded comparison proved equivalent and never executed, alongside an ordinary killed one | `./bin/mutation-judge --no-cache --operators boundary ./examples/equivalent` | 1 killed, 1 equivalent |
| `boolean` | Deleting either side of `&&` and attributing each kill | `./bin/mutation-judge --no-cache --operators boolean ./examples/boolean` | 2 killed |
| `test_selection` | The verdict depends on the selected test command | `./bin/mutation-judge --no-cache --operators boolean --test-run '^TestVIPDiscount$' ./examples/test_selection` | 1 killed, 1 survived |
| `arithmetic` | Numeric kills and a compile-invalid string mutation | `./bin/mutation-judge --no-cache --operators arithmetic ./examples/arithmetic` | 2 killed, 1 invalid |
| `generated` | Generated source excluded by default and included explicitly | `./bin/mutation-judge --no-cache --operators boolean --include-generated ./examples/generated` | 4 killed |
| `incremental` | Mutation candidates restricted to changed Git lines | `./bin/mutation-judge --changed HEAD ./examples/incremental` | Depends on the current diff |
| `errorreturn` | A swallowed error (`err` replaced with `nil`) that a weak test never notices | `./bin/mutation-judge --no-cache --operators errorreturn ./examples/errorreturn` | 1 survived |
| `switch` | Deleting a `case` clause: killed, an untested case surviving, and `default` removal breaking compilation | `./bin/mutation-judge --no-cache --operators switch ./examples/switch` | 1 killed, 1 survived, 1 invalid |
| `loop` | Forcing a `for` loop's condition false, and breaking out of a `range` loop's body immediately | `./bin/mutation-judge --no-cache --operators loop ./examples/loop` | 1 killed, 1 survived |
| `channel` | A buffered channel's capacity replaced with 0, probed safely with `select`/`default` | `./bin/mutation-judge --no-cache --operators channel ./examples/channel` | 1 killed |

The examples are intentionally small enough that each mutant can be reasoned about manually. The exact timing fields vary by machine; mutant IDs remain stable only for the same path, source offset, original text, and replacement.

## Before/after boundary pair

`boundary` and `boundary_fixed` contain the same production behavior. Their only meaningful difference is test coverage of `n == 0`. Compare the reports to see how one additional input changes the verdict from `SURVIVED` to `KILLED`.

## Full versus filtered tests

Run the complete `test_selection` suite:

```bash
./bin/mutation-judge --no-cache --operators boolean ./examples/test_selection
```

Then run only the VIP test:

```bash
./bin/mutation-judge \
  --no-cache \
  --operators boolean \
  --test-run '^TestVIPDiscount$' \
  ./examples/test_selection
```

The second run deliberately leaves the coupon behavior unobserved.

## Generated code

Default selection:

```bash
./bin/mutation-judge --no-cache --operators boolean ./examples/generated
```

Explicit generated-source selection:

```bash
./bin/mutation-judge \
  --no-cache \
  --operators boolean \
  --include-generated \
  ./examples/generated
```

## Incremental mode

This example needs a Git checkout and a local edit. Add parentheses to the return expression in `incremental/parser.go`, then run:

```bash
git diff --unified=0 HEAD -- examples/incremental/parser.go
./bin/mutation-judge \
  --changed HEAD \
  --operators boundary,boolean \
  ./examples/incremental
```

Only mutation spans overlapping the changed current-source line are retained.

## Report formats for CI

`--format sarif` and `--format github` reuse any example; the ones in `run-all.sh` demonstrate them against `boundary` (one survivor) and `boundary_fixed` (a clean, fully-killed run) so you can see both an actual finding and the "nothing to flag" case:

```bash
./bin/mutation-judge --no-cache --operators boundary --format sarif ./examples/boundary
./bin/mutation-judge --no-cache --operators boundary --format github ./examples/boundary
./bin/mutation-judge --no-cache --operators boundary --format github ./examples/boundary_fixed
```

SARIF output is meant for `github/codeql-action/upload-sarif`, turning survivors into GitHub code scanning alerts. `--format github` writes workflow-command annotations straight to stdout for inline PR annotations with no upload step. Both only report `SURVIVED`, `TIMEOUT`, and `UNKNOWN` verdicts -- see `docs/tutorial.md` section 15 and `docs/semantics.md`.

## Tracking survivors across runs

`run-all.sh`'s "compare and trend" section is self-contained: it copies `boundary`'s production file into a scratch module, analyzes it once with the checked-in weak test (a real survivor), patches only the test file in place, and analyzes again -- since the production file never moves, the mutant's ID stays the same between the two reports, so the comparison shows a genuine fix (`fixed_survivors`), not just two unrelated packages that happen to share a mutation pattern:

```bash
./bin/mutation-judge compare --baseline baseline.json --current current.json
./bin/mutation-judge record --label "PR #101" baseline.json
./bin/mutation-judge record --label "PR #102" current.json
./bin/mutation-judge trend
```

A second, separate scratch module right after it (`compare: a removed mutant, not a fix`) shows the other half of the distinction: this time the production code itself is deleted between the two runs rather than its test improved, so the same survivor lands in `removed_mutants` instead. `compare` treats these as genuinely different -- a fix is a real test-quality signal; a removed mutant is code churn with nothing to say about test quality either way, and folding it into "fixed" would overstate what actually happened.

`compare --format json` gives six buckets (`new_survivors`, `fixed_survivors`, `still_open`, `reclassified`, `removed_mutants`, `unchanged_count`) as clean, always-present fields -- list buckets serialize as `[]` rather than `null` when empty, so a CI script never needs a null-check before iterating.

`compare` and `record`/`trend` are subcommands (they come before any flags), and they only read existing `--format json` reports -- no analysis runs, so they work on any two reports you already have. See `docs/tutorial.md` section 16, in particular the mutant-ID matching caveat before relying on `compare` across a file a change actually edits.
