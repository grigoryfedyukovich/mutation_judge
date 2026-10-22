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
path): `Digest` SHA-256-hashes every file that walk visits (including its permission bits, type, and directory entries) — not just
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
The heavier, coverage-guided alternative this section describes is now
available too, as its own separate, stackable opt-in flag -- see
"Coverage-attributed per-test selection" below for what it costs and
guarantees.

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
cache key. A later audit exposed another collision: identical relative patterns
(e.g. `./...`) select different packages depending on the invocation
directory, even for the same mutant. Execution cache identity now uses
structured JSON containing the module-relative execution directory, actual
per-mutant patterns and `-run` expression, and the resolved package universe;
this is versioned separately so unsafe older entries never match. The
per-mutant diagnostic records the effective command and execution directory
for replay, including cache hits.

## F03 cache-input safeguards

The v3 execution key contains an SHA-256 hash of the frozen effective test
process environment; this includes user-defined test variables, not only the
Go toolchain variables. Changing a regular file's executable bits also changes
`workspace.Digest`, even when its bytes remain identical. Empty directories
have distinct digest entries. Local module replacements and external Go flag
files (`-modfile`, `-overlay`, `-toolexec`) are not part of the copied module
snapshot, so mutation result caching is disabled conservatively when they are
present. A report warning explains why. Snapshot copying still excludes the
paths listed above; tests depending on uncaptured external inputs should use
`--no-cache`. As of the R01 repair, main-module `go.mod replace` targets with
local filesystem paths are copied into the immutable input snapshot and
rewritten **only in the sandbox go.mod**. A fresh copy of that snapshot,
including external replacement contents, is used for every baseline,
profiling, enumeration, and mutant run; the original replacement directories
are not test execution inputs. Cache reuse remains deliberately disabled for
local replacements until the complete dependency graph is included in the
cache contract. The extra copies can increase execution time and disk usage.
Unsupported replacements (e.g. targeting an ancestor of the main module, or
an excluded/uncopyable module directory) return an explicit preparation error
instead of silently running against a shared host dependency tree.

Coverage-producing commands (baseline and optional per-test profiles) use
`-covermode=atomic` rather than `count`. Go requires atomic instrumentation
when running with the race detector (`GOFLAGS=-race`); the same mode remains
valid without `-race`. This may be more expensive than `count` coverage, but
ensures a consistent valid execution regardless of ambient Go race settings.
Non-coverage mutant executions do not add either flag.

## Coverage-attributed per-test selection (`--coverage-test-selection`)

Opt-in (default off), independent of `--narrow-test-scope` and stackable on top of it: narrows a mutant's `go test -run` pattern to ordinary top-level `Test...` functions whose own baseline coverage reaches the mutant's span **plus all runnable `Example...` and `Fuzz...` units in potentially observing packages**. Examples and fuzz seed corpora are preserved even when their coverage is not measured per unit. This prevents a coverage-based false `SURVIVED` verdict when an example output check or fuzz seed is the only mutation killer.

This is the "much heavier machinery" the `--narrow-test-scope` section above named and deliberately deferred: Go's aggregate `-coverprofile` data says a line executed at least once somewhere in a test run, never by which test, so getting per-test attribution means running every top-level test individually, each with its own fresh coverage profile. Concretely, before mutant execution begins, mutation-judge:

1. Runs `go test -list '^(Test|Example|Fuzz)'` once per test-bearing package (a package with no test files of its own is skipped) to enumerate runnable tests, examples, and fuzz targets. Subtests (`t.Run`) are not separately enumerated or attributed — a subtest's coverage is attributed to its enclosing top-level test. Examples and fuzz targets are remembered as package-qualified must-run units rather than considered uncovered.
2. Runs `go test -run '^Name$' -coverpkg=<resolved module test targets> -coverprofile=...` once per enumerated test, and parses the resulting profile. Explicit `-coverpkg` ensures tests in a dependent package (for example, package `b` importing production package `a`) can attribute coverage to source in `a`. Package-qualified test identities are preserved; duplicate test names across packages are selected conservatively.
3. For a given mutant, computes the potential observing test packages from `go list -deps -test`, unions ordinary tests covering any line in the mutant's span **and all runnable examples and fuzz targets** from those packages, and narrows that mutant's `-run` to a pattern such as `^(TestA|ExampleF|FuzzBoundary)$`. If any potentially observing test lacks an unambiguous instrumented file entry, it retains the original full test scope instead of assuming the missing row means no observation. Zero-count profile blocks are retained to distinguish known-uncovered from missing attribution. Profiles must contain a valid Go `mode: set|count|atomic` header and valid span/statement/count fields on **every** block; malformed or truncated profiles reject the attribution map rather than silently dropping rows. The parser also rejects implausibly large block spans instead of allocating unbounded line maps. If there are no profiled ordinary tests, selection also falls back to the full test scope.

The added cost is real and roughly proportional to the number of top-level tests, not the number of mutants — it is a fixed, one-time cost paid once per analysis run, independent of how many mutants exist. It pays off precisely when there are many more mutants than tests and mutants routinely trigger far more of a large test suite than actually needs to run for that specific line, the same shape of situation `--narrow-test-scope` already targets at package granularity; a suite with few tests, or one already narrow per package, will not see much benefit and may simply pay the per-test profiling cost for little gain — try both flags and compare, the same advice the `--narrow-test-scope` section above gives for the same reason. That upfront cost is not currently broken out as its own field in the report's `timing` block (`parse`/`baseline`/`mutants`/`render`/`total`); it is real wall-clock time included in `total` but not itemized in the other four, the same way the sandbox-copy and `go list` cost inside preparation already isn't. This is a known, accepted gap in the timing breakdown's completeness, not a hidden cost with no accounting at all.

**Soundness.** Never running a test against a given mutant is safe exactly when that test cannot possibly observe the mutation -- and a test that never executes the mutated line at all, through however many layers of call depth, cannot observe anything different about it, since coverage instrumentation records instrumented lines a test process actually runs. `-coverpkg` now instruments the selected production packages in dependent-package test binaries; a dependency-derived observer scope plus complete, unambiguous coverage records is required before eliminating tests. The one thing this safety argument depends on is that running each test *individually* faithfully reflects what it *would* cover as part of the full suite. Two failure modes of that assumption are handled explicitly, both by disabling the feature for the entire run and adding a `Warnings` entry rather than trusting a partial result:

- **A test that fails when run alone**, even though the full baseline (already validated before this phase ever runs) passed it. This is a real, not hypothetical, thing to guard against — order-dependent or shared-mutable-state tests exist even though idiomatic Go testing discourages them — and a dedicated regression test (`TestBuildPerTestCoverageFailsWhenATestFailsInIsolation`) constructs exactly this scenario (one test's package-level state that a second test depends on) and confirms it is caught.
- **Any other error** building the map at all — a package's tests failing to list, a coverage profile failing to parse.

What this does *not* try to detect: a test that would cover a line only when run as part of the full suite, in some way that doesn't also make it *fail* alone (for instance, a purely additive side effect a later assertion never checks). This is the same class of risk any statement-coverage-based test-selection or test-impact-analysis tool carries, and idiomatic, independent Go tests are not exposed to it in the first place; it is disclosed here rather than silently assumed away. One further constraint: a person who has already set their own `--test-run` disables this feature entirely for the run, since narrowing further on top of an arbitrary existing regular expression is not something a single resulting regular expression can safely express — two independent restrictions do not compose into one pattern in general — so the choice is to respect the person's own selection unchanged rather than approximate an intersection.

## Parallel isolated workers (`--workers`)

Opt-in (`--workers N`, default `1`) concurrent mutant execution. All
executions, sequential and parallel, use the same single prepared snapshot
as the input to a new independent writable sandbox **for every mutant**.
Workers no longer reuse directories between mutants; baseline tests, test
listing, and per-test profiling also operate on disposable fresh copies.
This prevents false kills and missing kills when tests mutate fixtures or
create files as side effects. Only read-only snapshot content and the
content-addressed cache are shared. The cost is one copy per execution,
not one per worker; reflink support can substantially reduce that cost.

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
  confirms an `Apply`/sandbox preparation failure in one worker (as opposed to an
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

**Cost**: Each mutant now requires a separate full sandbox copy, regardless
of `--workers`. Up to W copies are live simultaneously, and copy overhead
is proportional to the number of actual mutant executions plus baseline
and profiling. This favors correctness over reuse; reflinks can make the
copying significantly cheaper on supported filesystems.

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

**Examples and fuzz seed safety (F06):** runnable examples and fuzz targets are enumerated alongside ordinary tests and unconditionally retained for every potentially observing test package. They are not individually coverage-attributed, so suites with many examples or fuzz seeds may see less test-selection speedup, but cannot lose their observations solely because they are not ordinary `Test...` functions. Regression fixtures verify both kinds of killers with and without package-scope narrowing. Coverage selection still cannot certify behavior mediated by uninstrumented external processes or state-dependent interactions.

## Bounded backend capture (F15)

`go test -json` stdout is now parsed incrementally rather than accumulated in
`bytes.Buffer`. Verdict tracking is independent of diagnostic evidence. The
runner retains up to 64 KiB of decoded output, 16 KiB of unstructured stderr,
256 KiB / 4,096 events of assertion and panic evidence, and 256 KiB for a
single JSON record. Test/package state is limited to 8,192 identities, each
with at most 512 bytes of identity text. Assertions and panic entries in the
report are capped at 128 each. The report's `output_truncated` field and
diagnostic evidence indicate when output/evidence was clipped.

The runner continues draining output after hitting a retention budget, so
late `fail` events are still observed. A malformed/oversized JSON record or
classification-state overflow makes a *negative inference* inconclusive:
failed subprocesses become `UNKNOWN` unless explicit failure evidence is
available. This avoids treating missing observations as valid test outcomes.
A successful subprocess exit can still be reported `SURVIVED`. Very long
individual output records may prevent reliable attribution; increasing the
record cap would increase peak resident memory. Subprocess output is not
retained to a file, and terminal/report output is abbreviated by default.

`RenderMeasured` now measures one discard render and streams a second pass to
the destination. This avoids constructing another whole-report buffer, at the
cost of a second serialization. This bounds *captured output* memory; it does
not enforce a total process RAM limit, nor prevent resident memory from
growing with the number of mutants/results or many concurrent workers.

Synthetic tests drive tens of thousands of events and multi-megabyte output
through small chunks, and an end-to-end Go fixture verifies a late failure
after a large stdout/stderr flood. These verify capture limits and verdict
correctness, not a universal operating-system peak-RSS bound.


## R04 — Preparation resource limits

Toolchain detection, Git comparisons, local replacement rewrites, and runnable
unit enumeration now use a bounded capture path. A command retaining more than
32 MiB of stdout (or 32 MiB combined stdout/stderr) fails with an explicit
`subprocess output exceeds capture budget` error. When stdout is captured
separately, stderr retention is bounded to 64 KiB and oversized stderr also
fails. Captures continue draining to avoid deadlocking a child that writes
beyond the limit. The cap is fixed in `internal/proc`; an unusually large Go
package graph or diff may fail rather than be partially analyzed.

The preparation phase defaults to a 2-minute wall-clock budget and accepts
`--preparation-timeout DURATION` or `preparation_timeout` in config. A separate
`--total-timeout DURATION` / `total_timeout` is opt-in and bounds the analysis
run, including individual test execution. `0` disables each budget. An expiry
is an error, not a successful zero-mutant or partial-score measurement.
Snapshot copying checks cancellation between files and while copying file
chunks; snapshot verification compares fixed-size chunks. The time needed
for file-system cleanup after cancellation remains outside a hard deadline.

**Not yet a whole-program RAM bound:** JSON package graphs and mutant results
are still accumulated in memory, though no single subprocess output capture
can grow without limit. This pass does not implement external result
checkpointing or incremental export. A 32 MiB capture cap is not a promise
that process RSS stays below 32 MiB (or any fixed constant).
