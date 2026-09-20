# Integer and string literal mutation

`Retry` loops `MaxRetries` (`3`) times, starting its counter at `0`; `NewBuffer` allocates `BufferSize` (`8`) bytes. `TestRetryStopsAfterThreeAttempts` only exercises `Retry`'s retry count, checked against a hardcoded `3` rather than the `MaxRetries` identifier itself. `Greeting` prefixes its argument with `"Hello, "`, falling back to `DefaultName` (`"guest"`) when given an empty string; `TestGreetingIncludesPrefix` only ever calls `Greeting` with a non-empty name. The `literal` operator replaces each integer literal with both its value plus one and its value minus one, and each non-empty string literal with `""`:

- `MaxRetries`'s two mutants (`3`→`4` and `3`→`2`) are both killed: `Retry` then calls `fn` a different number of times than the test's hardcoded expectation.
- The loop's own init value (`i := 0`) is not inside its condition or post clause, so it's mutated too, and both of *its* mutants (`0`→`1` and `0`→`-1`) are killed the same way, by changing the iteration count.
- `"Hello, "`'s mutant is killed: `Greeting("Ann")` then returns `"Ann"` instead of `"Hello, Ann"`.
- `BufferSize`'s two mutants (`8`→`9` and `8`→`7`) and `DefaultName`'s mutant (`"guest"`→`""`) all survive: none of `NewBuffer`'s length, or the empty-name fallback path, is ever exercised by a test.

```bash
./bin/mutation-judge --no-cache --operators literal ./examples/literal
```

A literal is never mutated when it sits inside a `for` loop's own condition or post clause, even nested inside a compound `&&`/`||` condition -- this applies equally to string literals (a loop like `for s != "done" { ... }` is exactly as susceptible to an exotic non-monotonic termination risk as an integer-equality loop is). Mutating a loop's decrement amount to zero (`i -= 1` becoming `i -= 0`) removes its only progress toward termination outright; a threshold shift inside the condition is excluded out of caution rather than because it's ordinarily as dangerous as a decrement-to-zero. A loop's *init* value, as `Retry`'s `i := 0` demonstrates above, carries neither risk and is mutated normally: it can only change how many iterations run, never whether the loop terminates at all. Separately, an import path (`import "fmt"`) is never emptied either -- not a timeout-safety concern, just a mutant that could only ever be a guaranteed, zero-information compile failure. See `docs/semantics.md`.
