# Architecture

```text
CLI/config
   |
   v
workspace discovery ---- git diff mapper
   |                         |
   v                         v
Go frontend ----------> span-based mutation model
                              |
                              v
                    analysis orchestration
                    /        |         \
             sandbox     coverage     cache
                    \        |         /
                              v
                         test backend
                              |
                              v
                     text / JSON / HTML
```

## Packages

- `cmd/mutation-judge`: CLI, exit-code policy, output destination, crash boundary.
- `internal/config`: strict flat TOML/YAML-subset configuration and validation; unsupported nested syntax is rejected.
- `internal/workspace`: module/package discovery, source digest, safe module copy, atomic apply/restore boundary.
- `internal/frontend`: Go AST candidate discovery lowered immediately to source-span replacements.
- `internal/gitdiff`: zero-context diff hunk parsing plus NUL-delimited, ignore-aware untracked Go source listing for changed-line mode (line 0 is the whole-file sentinel).
- `internal/coverage`: baseline Go coverage-profile mapping.
- `internal/runner`: narrow execution backend interface plus the concrete `go test` backend.
- `internal/cache`: content-addressed, schema-versioned backend result cache.
- `internal/analysis`: orchestration, classification diagnostics, scoring, timing, and trust statements.
- `internal/report`: deterministic text, versioned JSON, and self-contained HTML rendering.

## Backend boundary

```go
type Backend interface {
    Run(context.Context, Request) Result
}
```

The analyzer does not directly construct processes outside the concrete backend. Unit tests use a deterministic fake backend, allowing classification and reporting to be tested without depending on subprocess timing.

Each mutant's `Request.Patterns` is the full pattern set the person supplied, unless `--narrow-test-scope` is enabled, in which case it's the minimal set of test packages that can actually observe that mutant's own package — computed once per analysis from the module's own dependency graph (`workspace.TestScopes`, via `go list -deps -test`, not a hand-walked `Imports` graph, since the latter misses a dependency reachable only through another package's external test file) and looked up per mutant (`analysis.mutantTestScope`). Any uncertainty in that lookup falls back to the full pattern set; see `docs/performance.md` for the correctness reasoning and the fixture built specifically to prove narrowing can't produce a false SURVIVED.

By default mutants run sequentially, each against a fresh sandbox copied from the same prepared snapshot. `--workers N` (N > 1) instead runs N concurrently, with a separate fresh sandbox for every mutant, sharing the same per-mutant execution logic (`Engine.runOneMutant`) as the sequential path so there is one place, not two independently-maintained copies, that decides cache keys and classifies results. Output ordering stays deterministic regardless of which worker finishes first: results are collected into a slice indexed by each mutant's position in the (already deterministic) discovery order, not completion order. See `docs/performance.md` for the concurrency-safety reasoning and how it was verified.

## Sandbox lifecycle


### R03: consistent preparation snapshot

Preparation copies and verifies the module tree **before** running package discovery, test-dependency analysis, or mutation discovery. These operations, including local-replacement directive parsing, use the captured bytes rather than reopening the live checkout. Local replacements are redirected into isolated dependency copies before `go list` runs. Source files, owning-package maps, test scopes, cache digest, and mutations therefore describe the same prepared tree.

For `--changed REV`, the Git revision and index are pinned before capture. Incremental changed-line and untracked-source discovery run in a temporary Git worktree materialized from the captured source, not the host checkout. Nested modules retain their parent ignore/attribute rules. An index or source change detected during capture produces an explicit error. An edit made **after** capture does not affect the current experiment. Filesystem copies are not transactional snapshots: undetectably transient concurrent edits or external nonmodule inputs remain outside the guarantee, and the consistency check adds a source-tree read during preparation.

The module is copied once into an immutable preparation snapshot. Discovery and source digest use this snapshot, never a live checkout. The baseline, test enumeration, individual per-test coverage runs, and every uncached mutant execution each receive their own fresh writable copy of that snapshot. Tests can add, delete, or modify arbitrary files without contaminating later verdicts. This trades extra sandbox-copy cost for correctness; reflink/clonefile support reduces the cost on supported filesystems.

The temporary copy excludes `.git`, `.mutation-judge`, and the configured cache path. Each file is copied via a copy-on-write clone where the platform and filesystem support one (Linux's `FICLONE` ioctl; see `docs/performance.md`), falling back to a full byte-for-byte copy otherwise — the fallback is always correct on its own, so this is purely a speed optimization, never a correctness dependency. General symlinks are preserved, but mutation targets are checked lexically and after symlink resolution; a source path that escapes the sandbox is rejected. Mutant application validates its source span against the original bytes captured at discovery, and uses same-directory temporary files plus atomic rename while preserving the original file mode. The disposable sandbox is removed after each run rather than restored for reuse. Cleanup runs on every normal/error return from orchestration, and also on SIGINT/SIGTERM: `cmd/mutation-judge` cancels an explicit context on either signal rather than leaving the process to Go's default (immediate-termination) disposition, which would otherwise skip the deferred cleanup entirely. An interruption additionally appends one record to `.mutation-judge/journal.ndjson` (timestamp, signal, phase, and progress) for post-mortem debugging beyond the exit code.

## Cache key

```text
SHA-256(
  CLI version,
  operator semantic version,
  invoked go toolchain (go env GOVERSION, GOOS, GOARCH, CGO_ENABLED, GOFLAGS),
  sandbox digest (workspace.Digest, including file type, permission bits, and empty directories),
  frozen effective child-process environment digest (no raw values),
  cache-relevant configuration JSON (timeout, test_run only — see below),
  backend name and version,
  mutant stable ID,
  replacement,
  the actual test patterns run for this mutant
)
```

The invoked toolchain component (`runner.DetectToolchain`, `internal/runner/runner.go`) is read once per analysis run via `go env`, never from `runtime.Version()` — the toolchain that happened to compile the mutation-judge binary itself, which is a different and sometimes different-versioned question (a cross-compiled CLI, or simply an upgraded/downgraded `go` on PATH since the binary was built, are both ordinary). Compiling with Go 1.22 and later running the same binary against a Go 1.25 `go` on PATH must not read back a Go-1.22-era cache entry: vet/language/runtime behavior can differ across toolchain versions, and GOOS/GOARCH/CGO_ENABLED/GOFLAGS are included alongside GOVERSION for the same reason — every one of them can change what a mutant's test run compiles to or how it behaves. This was a real, found bug (not a hypothetical): the key used to carry `runtime.Version()` twice over — once as a bare component, once again via `GoTest.Version()` feeding the backend-version component below — while the actual invoked toolchain was absent from the key entirely; see `ISSUES.md`.

`workspace.Digest` fingerprints every file `workspace.CopyModule` places into the sandbox — not just `*.go` / `go.mod` / `go.sum` / `go.work` / `go.work.sum`, but `//go:embed` payloads, cgo `.c`/`.h`/`.s` sources, `testdata/` fixtures, `go.env`, and anything else a test can read by path — via one shared file-selection walk (`workspace.sandboxEntries`) both functions use, rather than two independently maintained lists. This too was a real, found bug: `Digest` used to hash only the narrower Go-specific list, so changing a non-Go input a test could still observe (an embedded asset, a cgo source, a testdata fixture) produced a cache hit with stale results; see `ISSUES.md`.

Only `Timeout` and `TestRun` from the full configuration are in the key, not the whole `Config.AsMap()` — those are the only two fields that actually reach a mutant's `go test` invocation (`analysis.cacheRelevantConfig`). This is narrower than it looks by accident: a `"workers"` key was added to `Config.AsMap()` purely so `--print-config` could show it, and initially the cache key used that same full map, so it silently became sensitive to worker count too — running the identical analysis with a different `--workers` value produced zero cache hits despite no mutant's actual outcome being affected by how many others ran alongside it. Found by testing the finished feature, not while building it; fixed by introducing this explicit minimal subset, with a permanent regression test confirmed to fail against the reverted code.

The last component matters even though it's also a function of other inputs (effective configuration, which includes `--narrow-test-scope`, and the sandbox digest, which captures the dependency graph shape): before it was added explicitly, two runs against the same unchanged source tree but different top-level pattern arguments could produce the same key despite the real `go test` command differing — a genuine, if narrow, pre-existing gap found while adding dependency-graph-guided scoping (see `docs/performance.md`), fixed independently of whether that feature is enabled.

The F03 cache identity revision (`mutation-judge-execution/v3`) binds verdicts to permission bits and the effective test environment in addition to source bytes. The effective environment is frozen at preparation and passed to baseline, coverage, and mutant `go test` processes; GOWORK is forced to `off`. Raw environment values are not reported or embedded in cache keys. A local `go.mod replace` target can reside outside the immutable snapshot; the analyzer therefore disables the result cache whenever any local replacement is declared (even when it happens to be inside the source tree). It also disables caching for GOFLAGS `-modfile`, `-overlay`, or `-toolexec` inputs that reference mutable external files. A warning explains the fallback. Arbitrary external files accessed by tests or mutable external toolchains remain outside the guaranteed cache-input contract; `--no-cache` is the conservative option for those projects.

The stored JSON also has an independent schema marker. This prevents old result layouts from being silently accepted.

The F04 cache-store schema (`mutation-judge-cache/v2`) also validates the **verdict** on both reads and writes. Only completed `KILLED`, `SURVIVED`, and `INVALID` backend conclusions are reusable. `UNKNOWN`, `TIMEOUT`, `UNSUPPORTED`, and `EQUIVALENT` cannot be written or read back as execution evidence; in particular `UNKNOWN` after cancellation or process-start failure must be retried. `TIMEOUT` is intentionally transient even with a fixed per-mutant time limit because machine load can change the outcome. Before committing any otherwise cacheable result, the store checks the execution context for cancellation (including once immediately before atomic rename). Interrupted runs retain their partial report and mark `complete=false`, even when the final in-flight mutant returned after cancellation. Legacy v1 entries are ignored, including seemingly successful ones. This policy does not turn input-untracked tests into safe cache candidates: the F03 limitations still apply.

## Orchestration phases

### F07: subprocess lifetime and output-pipe cleanup

On Linux, macOS, and supported BSD systems, each Go test, test-listing,
Go-toolchain discovery, package/preparation query, and incremental Git diff
runs in its **own process group**. Deadline expiry or parent cancellation
kills the group, including ordinary descendants that inherited stdout/stderr.
A finite `250ms` `exec.Cmd.WaitDelay` bounds output-pipe draining after a
process exits or is canceled. After the subprocess returns, remaining members
of its group are also terminated (including when the direct child exited
normally). The effective bound for a test run is its requested timeout **plus
up to 250ms of pipe cleanup**, not the timeout alone. An exited parent with
lingering output writers is treated as a command failure rather than a
successful test run. A canceled test remains `UNKNOWN`; an elapsed test
budget remains `TIMEOUT`, and neither is reusable from cache.

On Windows, the same command wrapper creates a `JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE`
Job Object and launches the direct child with `CREATE_SUSPENDED`. It assigns
the suspended child to the Job Object before resuming execution, avoiding a
window in which a child could spawn an untracked descendant. Cancellation
terminates the job; after `Wait`, closing the job terminates remaining
members even when the direct child exited normally. Windows `WaitDelay`
retains the same 250ms pipe-cleanup limit. If the OS refuses Job Object
creation, assignment, or resumption (for example because of nested-job
restrictions), the runner kills/reaps the child and returns an error instead
of proceeding without descendant containment. The Windows path uses the
Windows-native `NtResumeProcess` export to resume the suspended process.
Windows lifecycle regressions run in a dedicated native CI job.

This group isolation is not a security sandbox: a process deliberately creating
another process group/session, or external daemons, can escape descendant
cleanup. On other operating systems, cancellation still terminates the direct
process and `WaitDelay` bounds inherited pipes, but group-wide termination is
not implemented. File-tree copying and parsing are not subprocesses and are
not made preemptible by this change. Preparation tool subprocesses honor the
analysis context, but preparation has no separate hard deadline.

`analysis.Engine` separates four phases: workspace/candidate preparation, baseline execution, serial mutant execution, and report construction. A cancellation after at least one completed mutant produces a report with `complete=false`; it is never mislabeled as a complete run.

## Diagnostic shape

Every result includes a verdict rule ID, precise source span, statement, source-diff/backend evidence, assumptions, and—only for survivors—a mutation-specific suggested test scenario.
