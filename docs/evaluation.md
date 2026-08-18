# Bounded self-hosting evaluation

**Date:** 2026-07-17
**Tool:** Mutation Judge 0.1.0
**Target:** `./internal/frontend`
**Command:**

```bash
mutation-judge \
  --no-cache \
  --operators boundary,boolean \
  --max-mutants 8 \
  --format json \
  --output self-eval.json \
  ./internal/frontend
```

## Initial result

```text
8 generated
2 killed
6 survived
0 invalid
0 timeout
score: 25.0% excluding invalid/timeout/unknown/unsupported
```

The run completed in bounded mode and the baseline passed before any mutant was executed.

## Interpretation

The initial ordering of candidates exposed three categories:

1. **Generated-file option behavior.** Mutations around `!IncludeGenerated && isGenerated(src)` survived because the original tests did not check both the default exclusion and explicit inclusion modes. A regression test was added immediately after this evaluation.
2. **Guarded sort comparisons.** Several `<` to `<=` mutants occur inside lexicographic comparators after explicit `!=` guards. For the guarded field, equality is unreachable at that comparison, making the boundary mutation observationally equivalent there. This is a useful reminder that a raw mutation score can be depressed by equivalent mutants.
3. **Defensive span validation.** Deleting one disjunct from the invalid-span check survived, indicating that malformed or zero-length source-span cases need more direct tests around the frontend/model boundary.

## Follow-up

The generated-file regression now verifies both default exclusion and `IncludeGenerated=true`. Re-running the same first eight mutants produced:

```text
8 generated
4 killed
4 survived
0 invalid
0 timeout
score: 50.0% excluding invalid/timeout/unknown/unsupported
```

All three generated-option mutants in this slice are now killed. The guarded comparator and malformed-span cases remain documented work rather than being hidden or counted as proven defects. A future equivalent-mutant suppression pass may recognize simple dominating inequality guards, but v0.1 intentionally does not claim equivalence reasoning.

This evaluation is self-hosting rather than a broad external corpus study. The next corpus milestone is to run a fixed operator/bound set on three small Go repositories and record runtime, invalid rate, survivor review, and manually confirmed equivalent mutants.

## v0.1.2 review-fix re-evaluation

**Date:** 2026-07-18
**Tool:** Mutation Judge 0.1.2
**Command:** the same bounded first-eight run, with `--progress=false` for a clean JSON artifact.

```text
8 generated
5 killed
3 survived
0 invalid
0 timeout
0 unknown
0 unsupported
score: 62.5% excluding invalid/timeout/unknown/unsupported
```

The report also records `62` candidates discovered before the deterministic bound, `8` retained after it, backend `go-test` with its Go version, and operator semantics `mutation-judge-operators/v1`.

The remaining three survivors are the guarded lexicographic comparator boundary mutations described above. The formerly surviving malformed-span branch is now killed by explicit reversed, zero-length, past-end, and path-escape regressions. This improves the bounded slice without suppressing likely equivalent comparator mutants or changing the score definition.
