# Statement deletion: covered but never asserted

`Scan` sets two fields. `TestScan` runs every line and checks the error and `Value`, but never reads `Valid`, so deleting `f.Valid = true` changes nothing the test can see. This is the shape of the google/uuid `TestNullUUIDScan` finding: the test "covers" `Scan` and still lets every `.Valid` assignment survive.

```bash
./bin/mutation-judge --no-cache --operators statement ./examples/statement
```

Expected essential result (derived by hand, not pasted from a run): 2 mutants, 1 killed, 1 survived.

- `delete assignment f.Value = src` is **killed**: `Value` stays empty and the `Value` assertion fails.
- `delete assignment f.Valid = true` **survives**: nothing reads `Valid`. The survivor's coverage is `covered`, which is exactly the point: coverage says the line ran, the mutant says nobody checked what it did.

To kill the survivor, assert `f.Valid` after a successful `Scan`.

The operator deliberately skips statements that would hang or end the run (`Lock`, `Wait`, `Close`, `panic`, ...), logging calls, anything inside a `go` statement or a loop it could influence, and any deletion that would leave a variable or import unused. See `docs/semantics.md`, "Statement deletion".
