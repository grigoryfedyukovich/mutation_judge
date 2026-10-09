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

A seventh opt-in operator, **literal**, mutates a `*ast.BasicLit`. For an integer literal it generates two mutants, its value plus one and its value minus one, regardless of the literal's original base (decimal, hex, octal, binary, or underscore-separated -- parsed with `go/constant`, not a naive `strconv` call, so all of those are read correctly; the replacement text itself is always plain decimal). A literal sitting exactly on the `int64` boundary is skipped rather than mutated, since `n+1` or `n-1` would silently wrap around in Go's own `int64` arithmetic at that exact edge. For a non-empty string literal it generates one mutant, replacing it with `""` (an already-empty string is left alone, since that mutation would be a no-op); `strconv.Unquote` decodes both interpreted and raw (backtick) string syntax to check for emptiness, so it isn't fooled by an empty-looking literal that is actually non-empty once escapes are decoded, or vice versa. An `*ast.ImportSpec`'s own path is never emptied, string or not: `import "fmt"` mutated to `import ""` is a guaranteed compile failure on essentially every Go file, since nearly every file imports something -- not a timeout-safety exclusion like the ones below, just a mutant that could only ever be a 100%-certain, zero-information `INVALID` verdict.

This operator needs two loop-safety exclusions of its own, both reusing the same `for`-statement condition/post tracking `relational` and `assignment` already build, and both apply to string literals exactly as they apply to integer ones (the underlying map is keyed by `*ast.BasicLit` regardless of kind): (1) inside a `for` loop's own post clause, a literal is exactly as dangerous as the operator it's an operand of -- `i -= 1` mutated to `i -= 0` removes the loop's only progress toward termination, the identical failure mode `assignment`'s own post-clause exclusion prevents at the operator level, just reached through the literal operand instead; (2) inside a `for` loop's own condition, ordinarily a literal shift is just as safe as a `boundary` shift (both just move a threshold by a bounded amount), but this pass cannot rule out an exotic non-monotonic loop whose termination depends on hitting an exact value (`for i != 10 { i *= 2 }`, or equally `for s != "done" { ... }`), so literal mutation is excluded from a loop's condition too, matching `relational`'s scope exactly rather than drawing a finer, harder-to-verify line. A loop's *init* value carries neither risk and is mutated normally -- it can only change how many iterations run, never whether the loop terminates at all.

### Statement deletion

The opt-in **statement** operator deletes one bare statement (`MJ-STMT-DELETE-ASSIGN`, `MJ-STMT-DELETE-CALL`). It exists for the gap coverage cannot see: a test that runs a line but never asserts on its effect. A `TestScan` that only compares the returned error "covers" `f.Valid = n > 0` and still lets its deletion survive.

Deletable: an assignment or `++`/`--` whose every target is a field selector, index expression, or pointer dereference, and a call statement. Every exclusion exists to avoid a guaranteed-uninformative verdict or an equivalence the tool cannot decide:

- **Never a bare identifier or `:=`.** Stores to locals and named results are frequently dead stores (see below); deleting one would spend a mutant on an equivalence not yet decided. `:=` would orphan every later use.
- **Calls that hang or end the run** are not deleted: `Lock`/`Unlock`/`RLock`/`RUnlock`, `Done`, `Wait`, `Signal`, `Broadcast`, `Acquire`/`Release`, `Close*`, `Shutdown`, `Cancel`/`cancel`, `Exit`, `Fatal*`, `Panic*`, `FailNow`, `Skip*`, and the builtins `panic`, `close`, `recover`, `print`, `println`; also `wg.Add(<integer literal>)`, whose deletion only panics on the matching `Done`. This is the same timeout-safety rule the `loop` and `channel` operators follow.
- **Logging calls** (`log.*`, `slog.*`, and methods named `Print*`, `Debug*`, `Info*`, `Warn*`, `Log*`) are not deleted: nobody asserts on them, so each would only survive.
- **Anything inside a `go` statement**, and anything inside a `for` loop whose termination it might influence: every statement in a loop with no condition or no post clause, and in a counted loop any statement mentioning a name from the loop header. `range` loops are bounded by their operand and are unaffected.
- **Anything whose deletion would leave a local variable or an imported package with no remaining read**, which the compiler rejects as a zero-information `INVALID`. Parameters are exempt from that check; package-level variables are treated like locals, which can only skip a legal deletion, never permit an illegal one.
- Only statements that sit directly in a statement list are considered, never a `for`/`if`/`switch` init or post clause.

Calls whose effect the operator cannot see (a custom wrapper that signals another goroutine, say) can still produce a `TIMEOUT`, reported as such.

### Condition negation

The opt-in **condition** operator replaces an `if` or `else if` condition `c` with `!(c)` (`MJ-COND-NEGATE`). It answers whether each branch was actually distinguished: a suite that never makes the condition false (or never true) lets the negation survive. It never negates a `for` condition; the `loop` operator owns those. Every exclusion avoids a guaranteed-uninformative `TIMEOUT` or runaway recursion, or a duplicate execution:

- **Loops.** Not in a `for` with no condition or no post clause (its exit may depend on the negated if); in a counted loop, not an if that mentions a name from the loop header; in a `range` loop, not an if containing `break` (over a channel that break may be the only exit).
- **Functions** containing `goto` (a goto loop's exit is an if) or calling themselves by name (negating a base case recurses until the runtime kills the process). Mutual recursion is not detected.
- **Concurrency.** Not an if whose condition or branches contain a channel send/receive, `select`, `go`, or a call excluded from statement deletion (`Lock`, `Wait`, `Done`, `Close`, ...), since negating the guard can starve a waiter forever.
- **Constants.** Not a bare `true`/`false` condition.
- **Duplicates.** Not a `!x` condition when `boolean` is enabled (`MJ-BOOL-DROP-NOT` yields the same program), nor a `==`/`!=` comparison when `relational` is enabled.

### Return-value replacement

The opt-in **returnvalue** operator replaces one returned expression with the zero value of its declared result type (`MJ-RET-ZERO`): `return n * 2` becomes `return 0`. It finds the test that calls a function and never looks at what it returns. Discovery is purely syntactic, so it acts only where the signature itself shows the type: the predeclared integer and float types (`0`), `string` (`""`), and slices and maps (`nil`). Each exclusion has a reason:

- `bool`: the constant can hang a caller's loop, and the boolean operators already cover it. `error`: owned by `errorreturn`.
- Pointers, funcs, channels, interfaces: a `nil` result just crashes the caller, a kill that says nothing about the tests.
- Named, generic, array and struct types: the zero value is not known without type information.
- Results that are already literals, `nil`, or negated literals (the `literal` operator owns those); empty slice/map literals and `make(...)` (nil is almost always equivalent).
- A return that forwards a multi-value call, has a different number of values than the signature, or is bare.
- A replacement that would leave a local variable or an imported package with no remaining use, which would not compile.

A caller that loops until a result becomes non-zero can still hang under this mutation; that is reported as `TIMEOUT`, not hidden.

### Logical-connective swap

The opt-in **connective** operator swaps `&&` and `||` (`MJ-CONN-SWAP`). The `boolean` operator drops an operand; this changes how the operands combine, which a suite with no case where exactly one operand is true cannot see. It is a separate operator so default `boolean` results do not change. Its exclusions mirror `condition`'s, but apply to a connective anywhere (a return, an assignment), not only in an if: not in a `for` loop's own condition; not in a loop with no condition or post clause, nor, in a counted loop, a connective mentioning a loop-header name; not in a function containing `goto` or calling itself by name; not in the condition of an if excluded from negation or touching concurrency; and not `x && x` / `x || x` over the same side-effect-free operand, which is the same program.

### Discarded result (returnvalue)

A `returnvalue` mutant (`MJ-RET-ZERO`) is proved `EQUIVALENT`, and never executed, when result `i` of a function is thrown away by every caller:

```go
func oom(n int) (int, int) { return n * 2, n + 1 }

_, m := oom(n)   // result 0 is discarded; zeroing `n * 2` is unobservable
```

Because the function must be unexported, every possible caller is in its own package directory, which is what keeps this a bounded scan rather than a whole-module call graph. Every restriction exists to keep the proof from being wrong:

- **Eligible functions:** unexported, package-level, non-generic, not `init`/`main`, with **unnamed** results (a deferred closure can read a named result after `return` assigns it, so "the caller discards it" would not mean "nothing observes it"). Methods are excluded: an unexported method can be called through an interface.
- **Every mention is a discard.** Every unqualified identifier in the package spelling the function's name must be the callee of a bare expression statement, or of the sole right-hand side of an assignment with exactly one left-hand side per result and `_` in position `i`. A function value, a `go`/`defer` call, an argument, a return, `(f)(x)`, a field or shadowing variable of the same name: any other mention blocks the claim, which can only make it more conservative.
- **At least one call site,** so a function that is simply never called is not reported as discarded.
- **Every same-package `.go` file is scanned,** test files and files excluded by build constraints included (a call site the current build does not compile could exist on another platform). A file that does not parse, an `import "C"`, an `//export` mentioning the name, an assembly file mentioning it, or a `//go:linkname` anywhere in the module mentioning it blocks the claim. The external `_test` package cannot reference an unexported name and is not counted.
- **The replaced expression must be pure** (identifiers, literals, and non-dividing, non-shifting arithmetic, ordered comparison and logic over them): replacing a division, call, selector, index or dereference would remove a panic or side effect the caller can observe even though it never sees the value.

Only `MJ-RET-ZERO` is covered. Arithmetic, relational and boundary mutants inside a discarded return expression are not claimed: swapping `*` for `/` can introduce a panic.

### Slice bounds

The opt-in **bounds** operator shifts one bound of a slice expression by one: the upper bound down (`s[:n]` -> `s[:n - 1]`, `MJ-SLICE-HIGH`) and the lower bound up (`s[k:]` -> `s[k + 1:]`, `MJ-SLICE-LOW`). It is the off-by-one check for slicing: a test that never inspects the exact extent of a returned slice lets either survive. A compound bound is parenthesized (`len(s)-1` -> `(len(s)-1) - 1`).

Only slice expressions are mutated, never `a[i]`: slice bounds are always integers, but an index may be a map key of any type, and `m[k + 1]` on a string-keyed map would not compile. Off-by-one on an integer *literal* index is the `literal` operator's job. Left alone:

- **Literal bounds and bounds with no identifier** (`s[:3]`), which the `literal` operator owns and where a constant shift can fail to compile (`s[:0 - 1]`); and bounds naming a constant declared in the same file, for the same reason. A constant declared in another file is not detected and may produce an `INVALID` mutant.
- **`go` statements** (a panic on another goroutine takes down the whole test binary, with no test to attribute it to), **functions containing `goto`**, and **loops a shifted bound could stop ending**: `for len(s) < n { s = s[:len(s)+1] }` never ends once the bound is lowered. A loop with no condition or post clause excludes every slice expression in its body; in a counted loop only one mentioning a loop-header name; `range` loops are bounded by their operand and are not excluded.

A shift can still panic (`s[k + 1:]` on a one-element slice). That is a kill by crash: the result carries `responsible_panics`, so it is visibly different from a kill by a failed assertion.

## Conservative equivalent-mutant suppression

Three locally provable equivalent-mutant shapes are recognized. The first, for the boundary operator, was first documented as a real finding rather than a hypothetical one in `docs/evaluation.md`'s "Guarded sort comparisons" (this project's own self-hosting evaluation) and confirmed again, unprompted, when this suppression was implemented -- see below:

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

### Dead-store boolean initializer

The boolean operator's literal mutation (`MJ-BOOL-LITERAL`) is suppressed for a `:=` declaration whose literal initializer is overwritten before it can be read. Two shapes, each requiring the overwrite to be the very next statement in the same statement list:

```go
seen := false
seen = probe(x)
```

```go
seen := false
if probe(x) {
	seen = true
} else {
	seen = false
}
```

After the next statement, the variable's value was decided entirely by an assignment that never reads it, and which assignment ran was decided by a condition that never reads it either, so the original and the mutant are in the same state in any program state. Every restriction exists to keep that proof from being wrong:

- The declaration is `:=` with one non-blank identifier and a bare `true`/`false` as the entire right-hand side.
- The overwrite is the immediately following statement, so nothing (including a label or goto target) can intervene.
- An overwrite is a plain `v = <expr>` (not `:=`, not a compound assignment) whose `<expr>` does not mention `v`.
- For the if/else shape: no init statement; the condition must not mention `v` (otherwise the two runs could take different branches -- `seen := false; if seen { seen = true } else { seen = false }` ends `false` originally and `true` when mutated); a plain `else` block (no `else if`, no missing else, since a path skipping both assignments still holds the literal); both blocks exactly one overwrite statement.

This is not a liveness analysis. A flag set only on some paths (`found := false` set to `true` inside a loop's `if`) is never matched, because its initializer is live on the other paths. Anything not matching exactly is generated and executed as an ordinary mutant. A suppressed mutant is marked `EQUIVALENT`, carries the reason (citing the overwriting statement's line and condition) as `equivalent_reason`, and is never executed. See `internal/frontend.detectDeadStoreLiteral`.

Not implemented: a discarded return value whose mutation never reaches an assertion. That needs every in-module call site of the function, so it is a heavier analysis and easier to get wrong.

## Trust and reproducibility

- The original working tree is read-only from the analyzer's perspective. Mutation writes are atomic inside a temporary copy, source path escapes are rejected, and source symlinks cannot redirect writes outside the sandbox.
- Test subprocesses receive argument arrays; no shell interpolation is used.
- Reports include the Mutation Judge version, operator-semantics version, Go runtime version, and backend identity/version.
- Cache entries are accepted only under the same cache schema and a key containing CLI version, operator-semantics version, source digest, configuration digest, backend identity/version, and mutant.
- Backend output is bounded in reports to avoid unbounded logs.
