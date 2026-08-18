# Boundary survivor

`CountPositive` uses `n > 0`, while the test covers only `2` and `-1`. Mutation Judge changes `>` to `>=`; the mutant survives because the missing distinguishing input is `0`.

```bash
../../bin/mutation-judge --no-cache --operators boundary .
```

From the repository root, use:

```bash
./bin/mutation-judge --no-cache --operators boundary ./examples/boundary
```

Compare with `../boundary_fixed` to see the repaired test suite.
