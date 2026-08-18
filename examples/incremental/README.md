# Incremental Git-diff mode

In a Git checkout, make a semantics-preserving edit to `parser.go`, such as adding parentheses around the lowercase range expression. Then run:

```bash
git diff --unified=0 HEAD -- examples/incremental/parser.go
./bin/mutation-judge \
  --changed HEAD \
  --operators boundary,boolean \
  ./examples/incremental
```

Only mutation spans overlapping added or modified current-source lines are retained. The archive may not contain Git history, so this example should be run in a clone or initialized repository.
