# Dead-store equivalence

`HasComma` declares `hasComma := false` and immediately follows it with an `if`/`else` that assigns `hasComma` on both branches, with a condition that never reads it. Whichever branch runs decides the value, so the initializer's `false` can never be observed. The boolean operator recognizes exactly this shape (a `:=` of a bare `true`/`false` whose very next statement overwrites the variable without reading it) and marks that mutant `EQUIVALENT` without running it, citing the overwriting statement as the proof.

The two assignments inside the branches are ordinary boolean mutants. `TestHasComma` exercises both a string with a comma and one without, so it kills both.

```bash
./bin/mutation-judge --no-cache --operators boolean ./examples/deadstore
```

Expected essential result: 3 mutants generated, 2 killed, 0 survived, 1 equivalent. The equivalent line carries a `proof:` explanation naming the `if` on the next line.

The match is deliberately narrow. It is not a liveness analysis: a flag that is only set on some paths (a loop that sets `found = true` when it sees something) keeps its initializer live and is never matched, and neither is an `else if` chain, a branch with more than one statement, or a condition that mentions the variable. See `docs/semantics.md`, "Dead-store boolean initializer", for each condition and why it exists.
