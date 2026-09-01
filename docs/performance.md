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
count. `Digest` and `CopyModule` now walk the module tree via the same
shared selection logic (`workspace.sandboxEntries`) and skip exactly
the same paths (`.git`, `.mutation-judge`, and the configured cache
path): `Digest` SHA-256-hashes every file that walk visits — not just
`.go` / `go.mod` / `go.sum` / `go.work` files, but `//go:embed`
payloads, cgo sources, `testdata/` fixtures, `go.env`, and anything
else a test can read by path (previously it hashed only the narrower
Go-specific list, which was a real correctness bug — changing one of
those other inputs produced a cache hit with stale results; see
`ISSUES.md`) — while `CopyModule` copies the same file set
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

These numbers were measured with `Digest`'s file selection unchanged
from when this benchmark was written; `generateModule`'s synthetic
fixture contains only `.go` files and `go.mod`, so widening `Digest` to
match `CopyModule`'s full file set (see above) does not change these
particular measurements — there is nothing else in this fixture for the
wider selection to pick up. A module with substantial non-Go content
under test (large `testdata/` fixtures, embedded assets) would see
`Digest`'s cost move closer to `CopyModule`'s than this table shows;
rerun the benchmark against a representative real module before relying
on these numbers for one.

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

## Dependency-graph-guided test scoping (`--narrow-test-scope`)

`ISSUES.md` named this item "coverage-guided package/test sharding."
What's implemented (`internal/workspace.TestScopes`,
`internal/analysis.mutantTestScope`) is deliberately **dependency-graph**-guided
rather than coverage-guided, and that's a considered choice, not a
shortcut: Go's basic `-coverprofile` data says a line executed at least
once *somewhere* in a test run, but not *which* test or package caused
it, so using it to narrow per-mutant scope safely would need much
heavier machinery (running tests individually or with extra
instrumentation to get per-test attribution, and handling edge cases
like table-driven subtests and shared setup code muddying that
attribution). The module's own import graph has no such ambiguity — `go
list` computes it exactly, and asking it directly (`go list -deps -test`)
rather than walking `Imports` by hand is what correctly captures a
dependency reachable only through another package's *external* test file
(a common pattern for integration-style tests), which a naive walk would
silently miss. That distinction was verified empirically against a
synthetic fixture (packages `a`/`b`/`c`/`d`/`e` with exactly this shape)
before any code was written, the same way the `go test -json` event
shapes were verified before `internal/runner`'s classifier was built.

**Design principle: any uncertainty widens back to the full pattern
set.** `mutantTestScope` never narrows unless both the mutant's owning
package and a computed scope for it are confidently known
(`internal/analysis/analysis_test.go`'s
`TestMutantTestScopeFallsBackWhenUncertain` tests each way that can
fail, directly). This is opt-in (`--narrow-test-scope`, default off) for
the same reason `docs/decisions/0001-config-parser-scope.md` favors
boring and explicit: the default behavior of a mutation-testing tool
should never change silently in a way that could, even in principle,
turn a real kill into a false survivor.

**The critical correctness property — that narrowing can never produce
a false SURVIVED — is proven end-to-end, not just unit-tested.**
`tests/integration/testdata/scoping` is a fixture built specifically to
break a naive implementation: package `a` has a boundary mutation its
*own* test doesn't catch, caught only by package `d`'s external test
file, which imports `a` in a way no non-test `Imports` walk would see.
`TestNarrowTestScopeKillsAcrossExternalTestOnlyDependency` confirms the
mutant is still correctly KILLED with scoping on. The fixture also
includes an unrelated package with a deliberately slow (3s), unrelated
test, giving a large, non-flaky timing signal (rather than a fragile
sub-100ms one) that scoping is doing something, not just safely doing
nothing: with scoping on, the mutant-execution timing phase specifically
stays under 3s; without it (the control test), it doesn't. Note that
*overall* wall time always pays that 3s cost regardless of scoping —
baseline always validates the full pattern set (checking the person's
whole test suite passes before any mutation happens is not something
scoping should ever skip), so only the mutant-execution phase, not the
whole run, is the correct thing to measure.

A related, genuine pre-existing gap was found and fixed while
implementing this: `cache.Key` never included which test patterns were
actually run for a given mutant, so two runs against the same unchanged
source tree with different top-level pattern arguments could, in
principle, collide on the same cache key despite the real `go test`
command differing. This existed before `--narrow-test-scope` (which
makes it more likely to actually matter, since the *effective* patterns
per mutant now also depend on the dependency graph, not just the
top-level arguments) but is a correctness question independent of it,
so it's fixed regardless of whether scoping is enabled — the actual
computed per-mutant scope is now its own explicit component of the
cache key.

## Parallel isolated workers (`--workers`)

Opt-in (`--workers N`, default `1` — sequential, byte-for-byte the same
code path as every earlier version) concurrent mutant execution. Each
worker gets its own fully independent sandbox (the same `CopyModule`
used for the single sequential sandbox, just called once per worker
instead of once per whole analysis — cheaply, where copy-on-write
cloning applies; see above), which is what makes concurrent execution
safe at all: nothing in the workspace, runner, or cache packages holds
shared mutable state, so two workers applying, running, and restoring
mutations on two *different* sandbox directories can never conflict.
That was established by reviewing `cache.Store` (stateless, one file per
key, atomic rename), `workspace.Apply`/`CopyModule` (no package-level
mutable state), and `coverage.Map.Covered` (read-only after the
sequential prepare phase completes, safe for concurrent reads) before
any concurrent code was written, not assumed afterward.

**This is the one item in this document where the environment's single
vCPU genuinely limits what can be verified — but not in the way it might
seem.** Real throughput improvement can't be measured here at all: with
one core, there's no way to distinguish "workers make this faster" from
"workers add scheduling overhead with no benefit," and reporting a
number either way would be closer to fabrication than measurement, so
none is reported. What *is* fully verifiable regardless of core count,
and was verified thoroughly rather than assumed: **correctness**. Go's
race detector instruments memory accesses and catches data races via
happens-before tracking whenever goroutines interleave at all, which
they do even on one core (particularly for I/O-bound work like spawning
`go test` subprocesses, which yield the OS thread while waiting) — this
is a genuinely meaningful test here, not a workaround for missing
hardware.

Verified:
- `go test ./... -race`, full repo, clean — including 8 repeated runs of
  the parallel-specific tests alone (`-race -count=8`) to catch anything
  intermittent, since race conditions aren't guaranteed to reproduce on
  the first try.
- **Determinism despite parallel, out-of-order completion**:
  `TestParallelExecutionIsDeterministicAcrossRuns` runs the same analysis
  three times with `--workers 3` against a fake backend with a small
  varying artificial delay (specifically to encourage different real
  completion orders across the three runs, not just permit them) and
  confirms identical result ordering every time. Output order is by
  each mutant's position in the deterministic discovery order, not
  whichever worker happened to finish first.
- **Equivalence with sequential execution, both against a fake backend
  and the real toolchain**: `TestParallelExecutionMatchesSequentialResults`
  (fake backend, content-derived verdicts so a mismatch would mean a
  result got attached to the wrong mutant/sandbox) and
  `TestWorkersProducesSameResultsAsSequential`
  (`tests/integration/testdata/workers`, a real fixture with two
  survivors and two kills) both confirm `--workers 3` and the sequential
  default produce byte-identical verdicts in identical order.
- **Cancellation**: `TestParallelExecutionCancellationProducesPartialReport`
  confirms a mid-run cancellation still yields a correct, non-hanging
  partial report (`Complete: false`, containing whatever did finish).
- **Hard-error propagation**: `TestParallelExecutionHardErrorStopsAllWorkers`
  confirms an `Apply`/restore failure in one worker (as opposed to an
  ordinary verdict) stops every worker promptly via an inner context
  derived from the run's own context — reusing the exact mechanism that
  already stops in-flight `go test` processes on SIGINT/SIGTERM, rather
  than inventing a second cancellation path.

**A real, independent bug was found and fixed while verifying this
feature, not while building it** — the value of testing what you built
rather than assuming it works: adding a `"workers"` key to
`Config.AsMap()` (needed so `--print-config` shows the setting) also
silently widened the cache key, which had been built from the *full*
`AsMap()`, to be sensitive to worker count. Running the identical
analysis with `--workers 3` and then `--workers 1` produced zero cache
hits on the second run despite every mutant's actual test outcome being
completely unaffected by how many other mutants happened to run
alongside it. Fixed by introducing `cacheRelevantConfig`, an explicit,
minimal subset containing exactly the two `Config` fields that actually
reach a mutant's `go test` invocation (`Timeout`, `TestRun` — everything
else, including patterns/scope, is already captured more precisely
elsewhere in the key). `TestCacheKeyIsInsensitiveToWorkerCount` locks
this in permanently, and was confirmed to fail against the reverted
code before being kept.

**Cost**: W workers means W independent sandboxes, multiplying the
`CopyModule` cost measured earlier by W — directly why the reflink work
above matters more with this feature enabled than without it, since it
turns that multiplication into a near-free one on filesystems that
support it.

The second performance item this measurement motivated: copy-on-write /
reflink sandbox creation.

## Copy-on-write / reflink sandbox creation

`CopyModule`
now attempts a copy-on-write clone (`internal/workspace/reflink_linux.go`,
Linux's `FICLONE` ioctl) before falling back to its original byte-for-byte
copy, on a per-file basis, with the fallback always correct regardless of
whether the clone succeeds.

**What's verified, and how:**
- The fallback path (what actually runs on this project's own CI matrix's
  `ubuntu-latest`/`macos-latest`, and on this development environment,
  since none of them use a reflink-capable filesystem by default) is
  covered by `TestCopyModuleProducesIndependentCopy`, which didn't exist
  before this work — `CopyModule` had previously only been exercised
  indirectly through higher-level tests.
- `tryReflink`'s failure paths (missing source, and — implicitly, since
  this sandbox's filesystem is ext4 — "FICLONE unsupported") are directly
  unit-tested and confirmed to never leave a partial file behind, which
  matters because the fallback copy path assumes `dst` doesn't already
  exist.
- A real, good-faith attempt was made to verify the actual clone *success*
  path (not just the fallback) in this environment: `xfsprogs` and
  `btrfs-progs` were installed, and a correctly-sized, reflink-enabled XFS
  image was created (`mkfs.xfs` confirmed `reflink=1` in its own output).
  Mounting it failed — `dmesg` confirms this container's kernel has
  neither XFS nor Btrfs filesystem support compiled in, and module loading
  isn't available (`modprobe` doesn't exist here). This is a hard
  environment limit, not a code problem to work around.
- `TestTryReflinkClonesOrCleanlyDeclines` is written to verify the one
  property that matters most — that mutating the clone can never affect
  the original, which is what makes it safe for `workspace.Apply` to
  mutate a reflinked sandbox file — but it can only assert that on a
  filesystem where a clone actually happens. On this sandbox it correctly
  and honestly `t.Skip()`s rather than silently passing or being
  disabled. **If you have access to a Linux machine with a btrfs volume,
  or can `mkfs.xfs -m reflink=1` and mount a loopback image (which
  requires kernel module access this container doesn't have), running
  `go test ./internal/workspace/ -run TestTryReflink -v` there would be
  genuinely valuable additional verification** this delivery couldn't
  provide itself.
- `GOOS=darwin GOARCH=arm64`, `GOOS=darwin GOARCH=amd64`, and
  `GOOS=windows GOARCH=amd64` cross-compiles of `./cmd/mutation-judge`
  all succeed, confirming `reflink_other.go`'s build-tag-gated stub is at
  least type-correct on every platform besides Linux — the one thing a
  cross-compile *can* confirm without being able to run the result.

**What's deliberately not implemented:** macOS's equivalent (`clonefile(2)`
via APFS). See the doc comment in `internal/workspace/reflink_other.go`
for the three real options considered (cgo, a `golang.org/x/sys/unix`
dependency, or a raw syscall by number) and why each has a real downside
— principally that this project has no way to test any of them, which is
the exact lesson just learned from the `internal/runner` `classifyEvents`
bug (a platform-specific behavior difference that only a real macOS run
caught). A syscall-level filesystem operation is a substantially
higher-stakes place to ship unverified platform-specific code than a text
classifier was.
