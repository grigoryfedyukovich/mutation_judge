# Arithmetic and invalid mutants

Numeric `+` to `-` and `*` to `/` mutants are killed by their tests. Mutating string concatenation from `+` to `-` does not type-check and is classified `INVALID`, so it is excluded from the score.

```bash
./bin/mutation-judge --no-cache --operators arithmetic ./examples/arithmetic
```
