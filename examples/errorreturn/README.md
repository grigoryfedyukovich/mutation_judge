# Error-return survivor

`Lookup` propagates `checkFound`'s error through `if err != nil { return 0, err }`. `TestLookupMissing` only checks the returned value on the missing-key path, never that the error itself is non-nil, so the `errorreturn` mutant (replacing `err` with `nil` in that return) survives: the value returned on that path is `0` either way.

```bash
./bin/mutation-judge --no-cache --operators errorreturn ./examples/errorreturn
```
