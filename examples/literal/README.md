# Integer literal mutation

`Retry` loops `MaxRetries` (`3`) times, starting its counter at `0`; `NewBuffer` allocates `BufferSize` (`8`) bytes. `TestRetryStopsAfterThreeAttempts` only exercises `Retry`'s retry count, checked against a hardcoded `3` rather than the `MaxRetries` identifier itself. The `literal` operator replaces each integer literal with both its value plus one and its value minus one:

- `MaxRetries`'s two mutants (`3`→`4` and `3`→`2`) are both killed: `Retry` then calls `fn` a different number of times than the test's hardcoded expectation.
- The loop's own init value (`i := 0`) is not inside its condition or post clause, so it's mutated too, and both of *its* mutants (`0`→`1` and `0`→`-1`) are killed the same way, by changing the iteration count.
- `BufferSize`'s two mutants (`8`→`9` and `8`→`7`) both survive: `NewBuffer`'s actual length is never checked.

```bash
./bin/mutation-judge --no-cache --operators literal ./examples/literal
```

A literal is never mutated when it sits inside a `for` loop's own condition or post clause, even nested inside a compound `&&`/`||` condition: mutating a loop's decrement amount to zero (`i -= 1` becoming `i -= 0`) removes its only progress toward termination outright, and even a threshold shift inside the condition is excluded out of caution for exotic non-monotonic loops. A loop's *init* value, as `Retry`'s `i := 0` demonstrates above, carries no such risk and is mutated normally: it can only change how many iterations run, never whether the loop terminates at all. See `docs/semantics.md`.
