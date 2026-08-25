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

All three generated-option mutants in this slice are now killed. The guarded comparator and malformed-span cases remain documented work rather than being hidden or counted as proven defects. A future equivalent-mutant suppression pass may recognize simple dominating inequality guards, but v0.1 intentionally does not claim equivalence reasoning. (That pass now exists -- see "Conservative equivalent-mutant suppression" below.)

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

The remaining three survivors are the guarded lexicographic comparator boundary mutations described above. The formerly surviving malformed-span branch is now killed by explicit reversed, zero-length, past-end, and path-escape regressions. This improves the bounded slice without suppressing likely equivalent comparator mutants or changing the score definition. (As above: suppression for exactly this guarded-comparator shape now exists -- see "Conservative equivalent-mutant suppression" below.)

## External corpus evaluation

**Date:** 2026-08-18
**Tool:** Mutation Judge 0.1.3
**Command (per target, only the pattern argument varies):**

```bash
mutation-judge \
  --no-cache \
  --operators boundary,boolean,arithmetic \
  --max-mutants 25 \
  --format json \
  --output <target>.json \
  --progress=false \
  ./...
```

Three small, real, dependency-free Go libraries, chosen for having no external
module dependencies (so `go build`/`go test` need no module-proxy network
access) and genuine, pre-existing test suites written by their own
maintainers rather than for this evaluation:

| target | version fetched | generated | killed | survived | invalid | score | wall time |
|---|---|---|---|---|---|---|---|
| [`github.com/rs/xid`](https://github.com/rs/xid) | `master` @ 2026-08-18 | 24 | 12 | 11 | 1 | 52.2% | 19.4s |
| [`github.com/dustin/go-humanize`](https://github.com/dustin/go-humanize) | `master` @ 2026-08-18 | 25 (of 172 discovered, bounded) | 15 | 8 | 2 | 65.2% | 18.9s |
| [`github.com/google/uuid`](https://github.com/google/uuid) | `master` @ 2026-08-18 | 25 (of 98 discovered, bounded) | 10 | 15 | 0 | 40.0% | 60.7s |

All three baselines passed before any mutant ran. `xid`'s 24 mutants is its
entire candidate set at this operator selection (not bounded); `go-humanize`
and `uuid` were bounded to the first 25 of 172 and 98 discovered candidates
respectively, in source order, so their scores describe that bounded slice
rather than the whole package.

Every survivor was reviewed against the actual target source rather than
just tallied, following the same self-hosting standard applied above: a
survivor is only called equivalent when the reasoning is concrete enough to
check, not merely plausible.

### xid — mostly environment-coupled, plus two real gaps

Eight of the eleven survivors cluster around `hostid_linux.go` and the
machine-ID fallback chain in `id.go`: code that reads `/etc/machine-id` and
`/proc/self/cpuset` from the real filesystem. Machine identity is inherently
environment-dependent, and the test suite reasonably does not mock these
paths, so these survivors are a defensible, expected coverage gap rather
than a defect — the same run on a machine with different `/etc/machine-id`
contents could kill some of these differently.

Two survivors are not environment-coupled and look like genuine gaps:

- `id.go:145`, the `XID_MACHINE_ID` environment-variable override's range
  check (`num < 0 || num > 0xFFFFFF`, both operands mutated) — a plain
  integer-parsing function with no test at any of the four interesting
  boundary values (`-1`, `0`, `0xFFFFFF`, `0x1000000`).
- `id.go:277`, `UnmarshalJSON`'s length guard (`len(b) < 2`) — no test
  exercises a 2-byte JSON input (e.g. `""`), the exact boundary between the
  guard rejecting input and calling into `UnmarshalText`.

### go-humanize — two provably equivalent mutants, three real gaps

- `big.go:30`, inside `oom()`: the float64 arithmetic on the function's
  first return value survives every mutation because it's provably
  unobservable — `comma.go:113` is the only call site in the module, and it
  discards that exact return value via `_, m := oom(c, athousand)`. This
  isn't a coverage gap; the value the mutation changes can never reach an
  assertion.
- `bigbytes.go:157`, `hasComma := false`'s initial value: also provably
  equivalent, for two independent reasons — `strings.Replace(num, ",", "",
  -1)` is a no-op when no comma is present regardless of the flag, and when
  a comma *is* present the scan loop (`if r == ',' { hasComma = true }`)
  overwrites the mutated initial value with the same `true` either way. No
  input to this function can make the initial value observable.
- `bigbytes.go:113` and `:120`, both `< 10` boundary checks controlling the
  raw-byte-count/formatting switch, and `bigbytes.go:117`'s
  `len(sizes)-1` magnitude cap: genuine gaps. No test in `bigbytes_test.go`
  exercises the value exactly `10`, and none drives the magnitude high
  enough to approach the `sizes` table's bound.

### uuid — one real test-suite bug, plus real gaps and an environment effect

This target's lower score is not evidence of weaker code; ten of its
fifteen survivors are one connected story once traced to source:

- **`null.go:37,43,47` (`NullUUID.Scan`) and `null.go:75`
  (`NullUUID.UnmarshalBinary`) are untested at the field level.**
  `TestNullUUIDScan` only compares the *error* returned by `Scan` against a
  plain `UUID.Scan`, never reading `.Valid` afterward, so all three of
  `Scan`'s `Valid` assignments survive being flipped. `UnmarshalBinary` has
  no dedicated test at all — `UnmarshalBinary` does not appear anywhere in
  `null_test.go` outside the source file itself.
- **`null.go:92,96` (`NullUUID.UnmarshalText`): `TestNullUUIDUnmarshalText`
  never calls `UnmarshalText`.** Reading the test body directly: despite
  its name, it calls `test.nullUUID.MarshalText()` twice and compares
  those results — an apparent copy-paste of the adjacent `MarshalText`
  test. This is worth flagging distinctly from an ordinary coverage gap:
  it's a real, present-day bug in a widely-used library's own test suite
  (`google/uuid` is a common dependency across the Go ecosystem), and it is
  exactly the kind of defect line-coverage tools cannot see — the line
  executes it just never checks anything, but `UnmarshalText` never
  executes at all, so no coverage tool would flag it either, since
  coverage only answers "did this line run", not "did anything check what
  it did."
- **`node.go:75,81` (`SetNodeID`)**: genuine gap. `uuid_test.go` calls
  `SetNodeID(id)` without capturing its return value and never calls it
  with an id shorter than 6 bytes, so neither the `true` nor the `false`
  return path is ever checked.
- **`node.go:41,53` and `node_net.go:28` (`getHardwareInterface` /
  `setNodeInterface`)**: environment-coupled, not a code or test defect.
  `getHardwareInterface("")` scans `net.Interfaces()` for any interface
  with a hardware address of at least 6 bytes; this container has at least
  one, so the interface-scan branch always succeeds and the `name == ""`
  random-fallback branch (`node.go:53`) is never reached. The
  `node_net.go:28` boundary mutant (`>= 6` to `> 6`) also survives here
  specifically because standard MAC addresses are exactly 6 bytes, so
  `> 6` matches zero real interfaces — but the two assertions in
  `uuid_test.go` (`SetNodeInterface("")` is true, `SetNodeInterface("xyzzy")`
  is false) end up true either way once the mutant forces both calls
  through the same fallback path, so the public-contract test doesn't
  distinguish "the real interface-scan mechanism worked" from "it silently
  failed and fell back." A CI runner with no usable hardware interface at
  all would very likely kill several of these differently.

### Takeaways

- All three raw scores (40–65%) are well below the internal `mutation-judge`
  self-hosting scores in this document, which is expected: those internal
  runs target code already hardened against a prior mutation-testing pass,
  while these are unmodified upstream libraries seeing mutation testing for
  the first time.
- The uuid run is the strongest evidence yet that survivor triage adds real
  value beyond the raw score: a flat 40% reads as "weak tests," but tracing
  every survivor to source turns up one connected, genuinely actionable
  defect (`UnmarshalText` never being called by its own test) plus a
  reasonable environment effect, not fifteen independent weaknesses.
- 3 of 25 go-humanize mutants and 0 of 25 uuid mutants were confirmed
  equivalent by concrete, checkable reasoning (traced to the single call
  site or the exact overwrite path) rather than by pattern (e.g. "looks
  like a defensive guard"), consistent with this project's stated position
  of not claiming general equivalence-detection.
- Runtime scales with the target's own test suite latency, not with mutant
  count alone: `uuid`'s 25 mutants took 60.7s (≈2.4s/mutant) against
  `go-humanize`'s 25 mutants in 18.9s (≈0.76s/mutant), tracking each
  package's own baseline `go test` duration rather than anything
  mutation-judge does per mutant.

This closes the corpus milestone this document named as pending: a fixed
operator/bound set was run on three small external Go repositories, with
runtime, invalid rate, and a full manual survivor review recorded above,
including concretely justified equivalent-mutant findings rather than
assumed ones.

## Conservative equivalent-mutant suppression

**Date:** 2026-08-24
**Tool:** Mutation Judge 0.1.3
**Command:**

```bash
mutation-judge --no-cache --operators boundary --progress=false ./internal/frontend
```

The "Guarded sort comparisons" finding from the very first evaluation in
this document, above, was documented by hand and left as a known,
unaddressed source of score depression -- see that section, and
`ISSUES.md`'s "Conservative equivalent-mutant suppression for locally
provable guarded comparisons" item. `internal/frontend.detectGuardedComparison`
now recognizes that exact shape (a comparison dominated by an
`if X != Y { return X < Y }`-shaped guard on the same two operands; see
`docs/semantics.md` for precisely which further conditions must all hold)
and classifies it `EQUIVALENT` instead of generating an ordinary mutant,
skipping execution entirely.

Re-running the same self-hosting target confirms this against the identical
real code the original finding described, not a synthetic restatement of
it:

```text
EQUIVALENT M-de7bcf87a167 internal/frontend/frontend.go:46:28 replace comparison < with <=
  coverage: not covered
  proof: dominated by the enclosing guard "all[i].Span.File != all[j].Span.File" (line 45): that check already establishes the two operands are unequal, so this comparison's strict/non-strict boundary can never be observed
EQUIVALENT M-508a02fe82d6 internal/frontend/frontend.go:49:33 replace comparison < with <=
  coverage: covered
  proof: dominated by the enclosing guard "all[i].Span.StartByte != all[j].Span.StartByte" (line 48): that check already establishes the two operands are unequal, so this comparison's strict/non-strict boundary can never be observed

summary
  20 mutants generated
  2 killed, 16 survived, 0 invalid, 0 timeout, 0 unknown, 0 unsupported, 2 equivalent
  score: 11.1% excluding invalid/timeout/unknown/unsupported/equivalent
```

Both comparisons in `Discover`'s own `sort.Slice` comparator -- the exact
lines the first evaluation traced by hand -- are now suppressed
automatically, with the actual dominating guard cited as the proof rather
than a bare `EQUIVALENT` label. The package's remaining sixteen survivors
are untouched: this pass only ever removes a mutant from the survivor
count when it can cite the specific guard that makes it unobservable, not
because it looks like defensive code.

This is deliberately the narrowest slice of "equivalent-mutant
suppression" that's still real: one exact pattern, checked structurally,
with execution skipped only where the proof holds. It does not generalize
to `go-humanize`'s `oom()`/`hasComma` findings from the external corpus
evaluation above (data-flow reasoning about a discarded return value and
an idempotent string replacement respectively) or to any equivalence
argument that isn't this one guarded-comparison shape -- see
`docs/limitations.md` limitation 7.

