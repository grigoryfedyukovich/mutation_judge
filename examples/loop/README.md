# Loop skip: killed and survived

`Sum`'s classic `for` loop has its condition forced to `false`; `TestSum` needs the loop to actually run to reach `6`, so that mutant is killed. `Max`'s `range` loop has its first body statement replaced with `break`; `TestMaxEmpty` only checks an empty slice, whose expected result (`0`) is exactly what you'd also get if the loop body never ran at all, so that mutant survives.

```bash
./bin/mutation-judge --no-cache --operators loop ./examples/loop
```
