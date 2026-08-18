# Boundary mutant killed

This package has the same production behavior as `../boundary`, but its table-driven test includes `n == 0`. That single case kills the `>` to `>=` mutant.

```bash
./bin/mutation-judge --no-cache --operators boundary ./examples/boundary_fixed
```
