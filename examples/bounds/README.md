# Slice bounds: the off-by-one check

The bounds operator shortens a slice expression's upper bound by one and advances its lower bound by one.

```bash
./bin/mutation-judge --no-cache --operators bounds ./examples/bounds
```

Expected essential result (derived by hand, not pasted from a run): 2 mutants, 1 killed, 1 survived.

- `shorten slice upper bound n by one` in `Head` is **killed**: `Head([]int{1,2,3}, 2)` now has length 1.
- `advance slice lower bound k by one` in `Tail` **survives**: `TestTailRuns` only checks that `Tail` does not panic.

A shift can panic too (`s[k+1:]` on a one-element slice). That is a kill by crash, and the report says so with a `crashed:` line instead of an assertion, so you can tell the tests noticed a crash rather than a wrong answer.

Only slice expressions are mutated, never `a[i]`: an index may be a map key of any type. Literal and constant bounds, `go` statements, `goto` functions, and loops a shifted bound could stop ending are skipped. See `docs/semantics.md`, "Slice bounds".
