# Equality/inequality mutation

`IsPending` checks `s == Pending`; `IsFinished` checks `s != Running`. `TestIsPendingDetectsPendingStatus` only exercises `IsPending`. The `relational` operator replaces `==` with `!=` (and back):

- `IsPending`'s `==` mutant is killed: with `!=` instead, `IsPending(Pending)` returns false (want true) and `IsPending(Running)` returns true (want false).
- `IsFinished`'s `!=` mutant survives: the function is never called by the test.

```bash
./bin/mutation-judge --no-cache --operators relational ./examples/relational
```

A `for` loop's own termination test is never mutated by this operator, even nested inside a compound `&&`/`||` condition: unlike a boundary (`<`/`<=`/`>`/`>=`) swap, which only ever shifts a monotonic threshold by one step, an equality/inequality swap inverts the test's polarity outright -- for a loop like `for x != target { ... }`, that can mean running zero times or never terminating, depending entirely on `x`'s actual trajectory. See `docs/semantics.md`.
