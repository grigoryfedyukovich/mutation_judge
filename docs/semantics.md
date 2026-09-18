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
| `EQUIVALENT` | Discovery itself proved the mutant behaviorally identical to the original, before any test ran; see "Conservative equivalent-mutant suppression" below. |

A killed mutant is evidence about the selected test command, not a proof that every behavioral difference is tested. A survivor is evidence that this concrete mutation was not distinguished by that command; it is not proof of a production defect. An `EQUIVALENT` result is different in kind from both: it is not evidence about the test command at all, since the command never ran -- it is a claim about the mutant itself, and Mutation Judge only makes that claim where limitation 7 in `docs/limitations.md` documents an exact, checked proof.

## Bounds

Reports include:

- `max_mutants`, where zero means no explicit candidate count bound;
- the per-command timeout;
- candidates discovered before the bound and candidates retained after it.

A timeout is never converted to killed or survived. Invalid, timeout, unknown, unsupported, and equivalent mutants are excluded from the score denominator.

## Coverage

Baseline statement coverage is collected once. It annotates whether the mutated source line overlaps a covered baseline statement. It is explanatory only: covered mutants are still executed, and uncovered mutants are not automatically labeled survived.

## Mutation operators

Boundary mutations alter strictness while preserving operands. Boolean connector deletion replaces a complete `&&` or `||` expression by either parenthesized operand. Negation deletion removes unary `!`; literal mutation flips boolean constants. Arithmetic mutation is optional because its semantic distance and invalid-mutant rate are higher.

Four further operators are opt-in, each targeting a Go-specific control-flow pattern rather than a single expression:

- **errorreturn** matches `if X != nil { ... return ...X }` -- an early return whose last result is exactly the value just checked against nil -- and replaces that returned value with `nil`, silently swallowing it. This pass carries no type information, so it cannot confirm `X` is specifically an `error`; anything else guarded and returned the same way is matched too, which is intentional, since the mutant is meaningful regardless of the checked value's exact type.
- **switch** deletes an entire `case` clause (its label and body together), including `default`, from a `switch` or type-switch statement.
- **loop** forces a `for` loop's body to never execute: a conditional `for` has its condition replaced with `false`; an unconditional `for {}` or a `range` loop has its first body statement replaced with `break`. Deliberately excluded is any mutation that could make a loop run forever (e.g. forcing a condition to `true`), since it produces a slow, uninformative TIMEOUT verdict on every occurrence rather than a fast KILLED or SURVIVED.
- **channel** replaces a `make(chan T, N)` capacity expression with `0` (buffered becomes unbuffered), and deletes an entire `select` `case` clause (comm statement and body together), including `default`. Deliberately excluded is deleting a `close(ch)` call: unlike the buffered-to-unbuffered mutation, which Go's runtime deadlock detector generally catches quickly if it stops all progress, a receiver still waiting on a channel that's never closed can block for the entire configured timeout with nothing left to detect, for the same reason the loop operator avoids ever-true conditions.

A fifth opt-in operator, **assignment**, targets compound-assignment and increment/decrement statements rather than a `BinaryExpr`: it swaps `+=`/`-=` and `*=`/`/=` (the same four operators and the same pairing `arithmetic` swaps at the binary-expression level, one level up, at the statement), and swaps `++`/`--`. It carries the identical loop-safety exclusion the `loop` and `channel` operators already need, for the same reason: the single most common home for an `IncDecStmt` is a `for` loop's own post clause (`for i := 0; i < n; i++`), and flipping the direction there doesn't produce a fast KILLED or SURVIVED -- for essentially any ordinary counting loop, it produces a guaranteed TIMEOUT instead, the same "runs forever" failure mode already excluded above. This operator refuses to mutate a statement that is exactly a `for` statement's own `Post` clause; a variable manually incremented inside a loop's *body* instead of its `Post` clause is not (and, short of data-flow analysis this project does not do, cannot cheaply be) detected the same way -- an honest, structural limitation, not an oversight, and the same kind of no-type-information disclaimer `errorreturn` already carries.

A sixth opt-in operator, **relational**, is Relational Operator Replacement restricted to equality: it swaps `==`/`!=` on a `BinaryExpr`, the one relational pair `boundary`'s `</<=/>/>=` swap does not already cover. It needs its own loop-safety exclusion too, for a different reason than `assignment`'s: a boundary swap only ever shifts a monotonic threshold by one step, so it cannot change whether a comparison eventually flips as a loop variable progresses in the same direction it always did -- but an equality/inequality swap inverts a comparison's polarity outright. A `for x != target { ... }` loop mutated to `for x == target` can run zero times or run forever depending entirely on `x`'s actual trajectory, which this pass has no way to know. This operator refuses to mutate any `BinaryExpr` appearing anywhere within a `for` statement's own condition, including nested inside a compound `&&`/`||` condition -- not just a bare top-level comparison. As with `assignment`, this is a purely structural, no-data-flow exclusion: it protects the loop's own termination test specifically, not every place a mutated comparison's result happens to influence control flow indirectly elsewhere.

A seventh opt-in operator, **literal**, mutates an integer literal (`*ast.BasicLit` with an integer `Kind`) into two mutants: its value plus one and its value minus one, regardless of the literal's original base (decimal, hex, octal, binary, or underscore-separated -- parsed with `go/constant`, not a naive `strconv` call, so all of those are read correctly; the replacement text itself is always plain decimal). A literal sitting exactly on the `int64` boundary is skipped rather than mutated, since `n+1` or `n-1` would silently wrap around in Go's own `int64` arithmetic at that exact edge. This operator needs two loop-safety exclusions of its own, both reusing the same `for`-statement condition/post tracking `relational` and `assignment` already build: (1) inside a `for` loop's own post clause, a literal is exactly as dangerous as the operator it's an operand of -- `i -= 1` mutated to `i -= 0` removes the loop's only progress toward termination, the identical failure mode `assignment`'s own post-clause exclusion prevents at the operator level, just reached through the literal operand instead; (2) inside a `for` loop's own condition, ordinarily a literal shift is just as safe as a `boundary` shift (both just move a threshold by a bounded amount), but this pass cannot rule out an exotic non-monotonic loop whose termination depends on hitting an exact value (`for i != 10 { i *= 2 }`), so literal mutation is excluded from a loop's condition too, matching `relational`'s scope exactly rather than drawing a finer, harder-to-verify line. A loop's *init* value carries neither risk and is mutated normally -- it can only change how many iterations run, never whether the loop terminates at all.

## Conservative equivalent-mutant suppression

The boundary operator recognizes exactly one locally provable equivalent-mutant shape, first documented as a real finding rather than a hypothetical one in `docs/evaluation.md`'s "Guarded sort comparisons" (this project's own self-hosting evaluation) and confirmed again, unprompted, when this suppression was implemented -- see below:

```go
if a.Field != b.Field {
	return a.Field < b.Field
}
```

Inside that guarded body, `a.Field != b.Field` already excludes equality, so `a.Field < b.Field` and `a.Field <= b.Field` (or `>` / `>=`) are the exact same relation there: the one case strict and non-strict comparison disagree on can never occur. Mutating the operator is therefore unobservable by any test, in any reachable state -- a real proof, not a heuristic pattern match.

The match is deliberately narrow, and every restriction exists specifically to rule out a way the proof could be wrong, not for simplicity:

- The `if` must have no init statement, so no variable the comparison relies on can be freshly introduced or shadowed by the guard itself.
- The guarded body must be exactly one statement -- a bare `return` of the comparison -- so nothing can reassign an operand between the guard's evaluation and the comparison's. There is no attempt to search a larger body for "the" dominated comparison.
- The guard must be a literal `X != Y` directly as the `if`'s condition (parens unwrapped): no `&&`/`||`, no `!(X == Y)`, no other logically-equivalent-but-differently-shaped form.
- Both operands must be side-effect-free -- identifiers, field selectors, index expressions, pointer dereferences, and literals only; no function or method calls, no channel receives -- and the comparison's two operands must be exactly the guard's two operands, in either order.

See `internal/frontend.detectGuardedComparison` for the implementation and `internal/frontend.isSideEffectFreeOperand`/`sameOperand` for the two structural checks it relies on. A comparison that doesn't match this exact shape is generated and executed as an ordinary mutant, same as before this suppression existed -- a missed equivalent mutant is a survivor a human can review; a wrongly suppressed one would be a false claim of certainty printed in a report, which is the failure mode this feature exists to avoid, not merely reduce.

A suppressed mutant is marked `EQUIVALENT` (see the verdict table above), carries the specific guard it was dominated by as its `equivalent_reason`, and is never executed at all -- not run and discarded, genuinely skipped, since there is no test outcome that could change a proof already established at discovery time. Confirming this against Mutation Judge's own source finds the exact case `docs/evaluation.md` originally described by hand: the `sort.Slice` comparator inside `internal/frontend.Discover` is now suppressed at both of its guarded comparisons.

## Trust and reproducibility

- The original working tree is read-only from the analyzer's perspective. Mutation writes are atomic inside a temporary copy, source path escapes are rejected, and source symlinks cannot redirect writes outside the sandbox.
- Test subprocesses receive argument arrays; no shell interpolation is used.
- Reports include the Mutation Judge version, operator-semantics version, Go runtime version, and backend identity/version.
- Cache entries are accepted only under the same cache schema and a key containing CLI version, operator-semantics version, source digest, configuration digest, backend identity/version, and mutant.
- Backend output is bounded in reports to avoid unbounded logs.
