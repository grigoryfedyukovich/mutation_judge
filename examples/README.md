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
| `boolean` | Deleting either side of `&&` and attributing each kill | `./bin/mutation-judge --no-cache --operators boolean ./examples/boolean` | 2 killed |
| `test_selection` | The verdict depends on the selected test command | `./bin/mutation-judge --no-cache --operators boolean --test-run '^TestVIPDiscount$' ./examples/test_selection` | 1 killed, 1 survived |
| `arithmetic` | Numeric kills and a compile-invalid string mutation | `./bin/mutation-judge --no-cache --operators arithmetic ./examples/arithmetic` | 2 killed, 1 invalid |
| `generated` | Generated source excluded by default and included explicitly | `./bin/mutation-judge --no-cache --operators boolean --include-generated ./examples/generated` | 4 killed |
| `incremental` | Mutation candidates restricted to changed Git lines | `./bin/mutation-judge --changed HEAD ./examples/incremental` | Depends on the current diff |

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
