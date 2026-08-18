# Boolean operand deletion

`ShouldRetry` requires both a non-nil error and a retryable error. Mutation Judge deletes each side of `&&` separately. `TestNilFailure` kills loss of the nil guard; `TestPermanentFailure` kills loss of the retryability predicate.

```bash
./bin/mutation-judge --no-cache --operators boolean ./examples/boolean
```
