# Condition negation: was each branch distinguished?

The condition operator rewrites an `if` condition `c` as `!(c)`.

```bash
./bin/mutation-judge --no-cache --operators condition ./examples/condition
```

Expected essential result (derived by hand, not pasted from a run): 2 mutants, 1 killed, 1 survived.

- `negate condition n < 0` in `Abs` is **killed**: negative and positive inputs both give the wrong sign.
- `negate condition trace != nil` in `Open` **survives**: `TestOpenRuns` passes a listener that does nothing, so skipping the call is invisible. The guard is covered, but no test asserts that the listener was told.

To kill the survivor, record the call in the listener and assert it fired.

The operator skips an `if` that could hang the run if negated: one inside a loop whose exit it might control, in a function containing `goto` or calling itself, or touching channels, `select`, `go`, or lock/wait/close calls. See `docs/semantics.md`, "Condition negation".
