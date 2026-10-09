# Discarded result: provably unobservable

`oom` is unexported, and its only caller, `Level`, throws the first result away with `_`. So zeroing the first result cannot change anything any caller sees. The returnvalue operator proves that and reports the mutant `EQUIVALENT` without running it.

```bash
./bin/mutation-judge --no-cache --operators returnvalue ./examples/discarded
```

Expected essential result (derived by hand, not pasted from a run): 2 mutants, 1 killed, 1 equivalent.

- `replace returned n * 2 with its zero value 0` is **equivalent**, with a `proof:` line saying result 1 of `oom` is discarded at all 1 call site.
- `replace returned n + 1 with its zero value 0` is **killed**: `Level` returns that result and `TestLevel` asserts it.

The proof is deliberately narrow. It needs an unexported, non-generic function with unnamed results; every mention of the name in the package to be a bare call or an assignment with `_` in that position (a function value, a `go` or `defer` call, or another caller using the result blocks it); at least one call site; and a replaced expression that cannot panic or have side effects. Test files and build-constrained files count as call sites. See `docs/semantics.md`, "Discarded result".
