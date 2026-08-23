# Semantic model and trust boundary

## Modeled behavior

Mutation Judge models a mutant as one deterministic byte-range replacement in one Go production source file. Candidate locations are discovered using Go's parser, but downstream analysis receives only a language-neutral record:

```text
(file, start byte, end byte, source line/column, original text, replacement text)
```

No execution or reporting component retains or compares Go AST node identities.

The concrete oracle is the selected local command:

```text
go test -count=1 -timeout DURATION [-run REGEXP] [-coverprofile FILE] PACKAGE...
```

A baseline command must pass. For each candidate, a temporary module copy contains exactly one replacement while the same selected tests execute.

## Verdict interpretation

| Verdict | Evidence |
|---|---|
| `KILLED` | The mutant command exited unsuccessfully for a test-semantic reason, including an assertion failure or runtime panic. |
| `SURVIVED` | The mutant command exited successfully. |
| `INVALID` | Compiler/type-check diagnostics or `[build failed]` were observed. |
| `TIMEOUT` | The Go test timeout or the enclosing process deadline expired. |
| `UNKNOWN` | The backend could not classify a run, such as process start failure or external cancellation. |
| `UNSUPPORTED` | Reserved for an input or operator a backend explicitly declines. |

A killed mutant is evidence about the selected test command, not a proof that every behavioral difference is tested. A survivor is evidence that this concrete mutation was not distinguished by that command; it is not proof of a production defect.

## Bounds

Reports include:

- `max_mutants`, where zero means no explicit candidate count bound;
- the per-command timeout;
- candidates discovered before the bound and candidates retained after it.

A timeout is never converted to killed or survived. Invalid, timeout, unknown, and unsupported mutants are excluded from the score denominator.

## Coverage

Baseline statement coverage is collected once. It annotates whether the mutated source line overlaps a covered baseline statement. It is explanatory only: covered mutants are still executed, and uncovered mutants are not automatically labeled survived.

## Mutation operators

Boundary mutations alter strictness while preserving operands. Boolean connector deletion replaces a complete `&&` or `||` expression by either parenthesized operand. Negation deletion removes unary `!`; literal mutation flips boolean constants. Arithmetic mutation is optional because its semantic distance and invalid-mutant rate are higher.

Four further operators are opt-in, each targeting a Go-specific control-flow pattern rather than a single expression:

- **errorreturn** matches `if X != nil { ... return ...X }` -- an early return whose last result is exactly the value just checked against nil -- and replaces that returned value with `nil`, silently swallowing it. This pass carries no type information, so it cannot confirm `X` is specifically an `error`; anything else guarded and returned the same way is matched too, which is intentional, since the mutant is meaningful regardless of the checked value's exact type.
- **switch** deletes an entire `case` clause (its label and body together), including `default`, from a `switch` or type-switch statement.
- **loop** forces a `for` loop's body to never execute: a conditional `for` has its condition replaced with `false`; an unconditional `for {}` or a `range` loop has its first body statement replaced with `break`. Deliberately excluded is any mutation that could make a loop run forever (e.g. forcing a condition to `true`), since it produces a slow, uninformative TIMEOUT verdict on every occurrence rather than a fast KILLED or SURVIVED.
- **channel** replaces a `make(chan T, N)` capacity expression with `0` (buffered becomes unbuffered), and deletes an entire `select` `case` clause (comm statement and body together), including `default`. Deliberately excluded is deleting a `close(ch)` call: unlike the buffered-to-unbuffered mutation, which Go's runtime deadlock detector generally catches quickly if it stops all progress, a receiver still waiting on a channel that's never closed can block for the entire configured timeout with nothing left to detect, for the same reason the loop operator avoids ever-true conditions.

## Trust and reproducibility

- The original working tree is read-only from the analyzer's perspective. Mutation writes are atomic inside a temporary copy, source path escapes are rejected, and source symlinks cannot redirect writes outside the sandbox.
- Test subprocesses receive argument arrays; no shell interpolation is used.
- Reports include the Mutation Judge version, operator-semantics version, Go runtime version, and backend identity/version.
- Cache entries are accepted only under the same cache schema and a key containing CLI version, operator-semantics version, source digest, configuration digest, backend identity/version, and mutant.
- Backend output is bounded in reports to avoid unbounded logs.
