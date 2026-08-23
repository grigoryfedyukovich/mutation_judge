# Switch case deletion

`Grade` has three case clauses. `TestGradeTopAndBottom` only exercises the top (`>= 90`) and bottom (`default`) cases, never the middle `>= 80` case, so deleting that one case survives -- no test's outcome depends on it. Deleting the top case is killed (it changes `Grade(95)` to `"B"`). Deleting `default` removes the switch's only unconditionally-returning branch, which Go's compiler rejects as a missing return -- an `INVALID` mutant, excluded from the score, same as the compile-invalid arithmetic mutant in `../arithmetic`.

```bash
./bin/mutation-judge --no-cache --operators switch ./examples/switch
```
