# Performance: digest and sandbox-copy cost

This records the first item in `ISSUES.md`'s performance-work list:
"Measure source-tree digest and full-sandbox copy cost on large modules
before narrowing either correctness boundary." The benchmarks themselves
live in `internal/workspace/workspace_bench_test.go` and are meant to be
rerun, not just read about — see that file for exact methodology and an
important caveat about measurement noise, summarized below.

## What's being measured, and why these two specifically

`workspace.Digest` and `workspace.CopyModule` are the two costs
`analysis.Engine.Analyze` pays exactly once per run, before baseline
execution and before any mutant runs — they do not scale with mutant
count. `Digest` walks the module tree and SHA-256-hashes every `.go` /
`go.mod` / `go.sum` / `go.work` file's content (used for the cache key).
`CopyModule` walks the whole module tree (everything except `.git`,
`.mutation-judge`, and the configured cache path) and copies it
byte-for-byte into a fresh temporary sandbox, preserving file modes and
symlinks. If either of these turned out to dominate a real run's wall
time on a large module, that would be the case for narrowing what gets
digested or copied — hence measuring first.

## Method

`internal/workspace/workspace_bench_test.go` generates synthetic Go
modules at four sizes (100 / 500 / 2000 / 8000 files, ~3KB/file, grouped
into packages of 20 files each to match typical real-project structure
rather than one giant package) and runs both operations as standard Go
benchmarks:

```bash
go test ./internal/workspace/ -bench . -benchtime 6x -run '^$'
```

## Results and an important caveat

`Digest` (read-only) was consistent to within a few percent across
repeated invocations and scaled cleanly with size:

| files | ~source size | Digest |
|---|---|---|
| 100 | 300KB | ~1–2ms |
| 500 | 1.5MB | ~5–7ms |
| 2000 | 6MB | ~21–23ms |
| 8000 | 24MB | ~93–100ms |

`CopyModule` (write-heavy: stat, create, read+write loop, chmod per
file, plus `MkdirAll` per new package directory) did **not** reproduce
consistently. Across separate `go test -bench` invocations on this
measurement environment (a single-vCPU sandbox with virtualized block
storage), the same 8000-file module measured anywhere from ~90ms to
over 1 second, and the relative ordering between the 2000- and 8000-file
sizes flipped between runs — one run showed clearly super-linear scaling
(13x cost for a 4x size increase), another showed sub-linear scaling
for the same two sizes. Repeating the *identical* 4000-file case back to
back in the same process also produced results that didn't match a
separate invocation's number for the same size (405ms vs. 862ms).

That inconsistency is itself the finding, and it points at storage I/O
noise rather than an algorithmic problem: `CopyModule`'s logic is a
straightforward single-pass `WalkDir` with one sequential copy per file —
there's no nested iteration over files or other obvious super-linear
structure to explain a real 13x-for-4x scaling. A read-only benchmark
(`Digest`) against the *same generated fixtures* stayed stable; only the
write-heavy one didn't. On a real developer machine or a well-provisioned
CI runner with local SSD/NVMe storage, this variance would very likely be
far smaller — but the fact that the write path is the one exposed to
whatever storage variance exists is true regardless of environment, and
is the direct motivation for the next item below.

## What this does and doesn't support

- **It does not support a claim that `CopyModule` has an algorithmic
  scaling problem.** The code has no structure that would produce
  super-linear behavior, and the observed non-monotonic results across
  runs are inconsistent with a real data-dependent effect — a genuine
  algorithmic problem would reproduce the same way every time for the
  same input.
- **It does support prioritizing copy-on-write/reflink sandbox creation**
  (the next item in `ISSUES.md`'s performance list) as the highest-leverage
  next step specifically because it targets the write path this
  measurement shows to be both the more expensive operation *and* the one
  exposed to storage-level variance. A reflink/clonefile-based sandbox
  (Btrfs/XFS reflink on Linux, APFS clonefile on macOS) would make
  sandbox creation close to O(1) regardless of module size on filesystems
  that support it, eliminating the write-heavy full copy rather than
  trying to make it faster.
- **It does not support narrowing what `CopyModule` copies** (e.g. only
  copying files needed to build the target packages) as a *correctness*
  change. Even the worst observed number here (~1s for a 24MB / 8000-file
  module) is a one-time cost, and for any real project large enough to
  have thousands of files, the baseline `go test` run that follows is
  very likely to take substantially longer than that — so narrowing the
  copy in the name of speed would trade away the sandbox-fidelity
  guarantee this project deliberately favors (see `docs/limitations.md`,
  "Conservative I/O") for a cost that isn't actually dominant in a real
  end-to-end run. If a future measurement against a real large project
  shows otherwise, that would be the evidence to revisit this.

## Reproducing

```bash
go test ./internal/workspace/ -bench . -benchtime 6x -run '^$'
```

Given the noise documented above, prefer more repetitions
(`-benchtime 10x` or higher) and multiple separate invocations before
drawing conclusions on unfamiliar hardware, especially for
`BenchmarkCopyModule`.
