# Compound assignment and increment/decrement mutation

`Ledger.Deposit` adjusts the balance with `+=`, `Withdraw` with `-=`, and `Tick` with `++`. `TestDepositIncreasesBalance` only exercises `Deposit`. The `assignment` operator replaces `+=` with `-=` (and back), `*=` with `/=` (and back), and `++` with `--` (and back):

- `Deposit`'s `+=` mutant is killed: with `-=` instead, two deposits of 5 and 3 leave the balance at -8, not 8.
- `Withdraw`'s `-=` mutant and `Tick`'s `++` mutant both survive: neither method is ever called by the test.

```bash
./bin/mutation-judge --no-cache --operators assignment ./examples/assignment
```

A `for` loop's own post-clause increment (`for i := 0; i < n; i++`) is never mutated by this operator, regardless: flipping its direction turns an ordinary counting loop into one that never terminates, which would only ever produce a slow, uninformative `TIMEOUT` rather than a `KILLED` or `SURVIVED` verdict. See `docs/semantics.md`.
