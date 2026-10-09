# Return-value replacement: called but never read

The returnvalue operator replaces a returned expression with the zero value of its declared result type, when the signature shows the type.

```bash
./bin/mutation-judge --no-cache --operators returnvalue ./examples/returnvalue
```

Expected essential result (derived by hand, not pasted from a run): 2 mutants, 1 killed, 1 survived.

- `replace returned n * 2 with its zero value 0` is **killed**: `Scale(3)` returns 0 instead of 6.
- `replace returned len(xs) with its zero value 0` **survives**: `TestCountRuns` calls `Count` and discards the result.

To kill the survivor, assert `Count`'s result.

Only results whose zero value is certain from the signature are touched: integers and floats (`0`), `string` (`""`), slices and maps (`nil`). `bool`, `error`, pointers, named and generic types are left alone. See `docs/semantics.md`, "Return-value replacement". For a case where this operator's mutant is *proved* unobservable, see `../discarded`.
