# Connective swap: is there a case where exactly one operand is true?

The connective operator swaps `&&` and `||`. (The `boolean` operator drops an operand instead; this changes how the operands combine.)

```bash
./bin/mutation-judge --no-cache --operators connective ./examples/connective
```

Expected essential result (derived by hand, not pasted from a run): 2 mutants, 1 killed, 1 survived.

- `replace logical connective && with ||` in `InRange` is **killed**: `InRange(0, 1, 10)` becomes true because `0 <= 10` alone suffices.
- The same swap in `Eligible` **survives**: with both operands true, or both false, `&&` and `||` give the same answer, and `TestEligible` uses only those two cases. Add `Eligible(20, false)` or `Eligible(10, true)` to kill it.

The operator skips a connective in a loop's own condition, in loops it could make endless, in functions with `goto` or direct recursion, and `x && x`. See `docs/semantics.md`, "Logical-connective swap".
