# Conservative equivalent-mutant suppression

`Less` compares two fields: `Priority`, guarded by `if a.Priority != b.Priority`, and `SubmittedAt`, the unguarded tie-breaker. Inside that guard, `a.Priority != b.Priority` already excludes equality, so `a.Priority < b.Priority` and `a.Priority <= b.Priority` are the exact same relation there -- the one case strict and non-strict comparison disagree on can never occur. The boundary operator recognizes this exact shape and classifies that mutant `EQUIVALENT` instead of generating an ordinary one: it is never executed, and the report cites the actual guard as the proof rather than a bare label.

The `SubmittedAt` comparison has no such guard, so it's an ordinary boundary mutant -- `TestLessBySubmittedAt` covers its `n == n` boundary directly, killing it.

```bash
./bin/mutation-judge --no-cache --operators boundary ./examples/equivalent
```

```text
EQUIVALENT M-a82d82c8f22d examples/equivalent/item.go:16:21 replace comparison < with <=
  coverage: covered
  proof: dominated by the enclosing guard "a.Priority != b.Priority" (line 15): that check already establishes the two operands are unequal, so this comparison's strict/non-strict boundary can never be observed
KILLED M-af698edd8dc9 examples/equivalent/item.go:18:23 replace comparison < with <=
  coverage: covered
  killed by: TestLessBySubmittedAt

summary
  2 mutants generated
  1 killed, 0 survived, 0 invalid, 0 timeout, 0 unknown, 0 unsupported, 1 equivalent
  score: 100.0% excluding invalid/timeout/unknown/unsupported/equivalent
```

This is the same pattern this project's own self-hosting evaluation found by hand in its `sort.Slice` comparator (`internal/frontend.Discover`) and documented as unaddressed score depression -- see `docs/evaluation.md`, "Conservative equivalent-mutant suppression." The match is deliberately narrow; see `docs/semantics.md` for exactly which conditions must all hold, and why each one exists specifically to rule out a way the proof could be wrong.
