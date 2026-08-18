# Generated-source selection

`predicate.go` is ordinary source. `generated_predicate.go` has the conventional generated-code header and is skipped by default.

```bash
./bin/mutation-judge --no-cache --operators boolean ./examples/generated
```

Include both files explicitly:

```bash
./bin/mutation-judge \
  --no-cache \
  --operators boolean \
  --include-generated \
  ./examples/generated
```
