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

The module is copied once into an immutable preparation snapshot. Discovery and source digest use this snapshot, never a live checkout. The uninstrumented baseline (the authoritative pass check), optional aggregate coverage profiling, test enumeration, individual per-test coverage runs, and every uncached mutant execution each receive their own fresh writable copy of that snapshot. Tests can add, delete, or modify arbitrary files without contaminating later verdicts. This trades extra sandbox-copy cost for correctness; Linux reflinks can reduce copying costs on supported filesystems.

The temporary copy excludes `.git`, `.mutation-judge`, and the configured cache path. Each file is copied via a copy-on-write clone where the platform and filesystem support one (Linux's `FICLONE` ioctl; see `docs/performance.md`), falling back to a full byte-for-byte copy otherwise — the fallback is always correct on its own, so this is purely a speed optimization, never a correctness dependency. The copy omits symlinks that resolve outside the module and rewrites absolute in-tree links relative to the sandbox. Mutation paths are checked before and after symlink resolution. Mutant application validates its source span against the original bytes captured at discovery, and uses same-directory temporary files plus atomic rename while preserving the original file mode. The disposable sandbox is removed after each run rather than restored for reuse. Cleanup runs on every normal/error return from orchestration, and also on SIGINT/SIGTERM: `cmd/mutation-judge` cancels an explicit context on either signal rather than leaving the process to Go's default (immediate-termination) disposition, which would otherwise skip the deferred cleanup entirely. An interruption additionally appends one record to `.mutation-judge/journal.ndjson` (timestamp, signal, phase, and progress) for post-mortem debugging beyond the exit code.

## Cache key

```text
SHA-256(
  CLI version,
  operator semantic version,
  invoked go toolchain (go env GOVERSION, GOOS, GOARCH, CGO_ENABLED, GOFLAGS),
  sandbox digest (workspace.Digest, including file type, permission bits, and empty directories),
  frozen effective child-process environment digest (no raw values),
  cache-relevant configuration JSON (timeout and test_run),
  backend name and version,
  mutant stable ID,
  replacement,
  module-relative working directory and resolved test-package universe,
  actual per-mutant test patterns and -run selector
)
```

The key uses the Go toolchain invoked by the CLI. It fingerprints effective
`go env -json` settings and the contents of the persistent `GOENV` file.
Reports expose the digest as `bounds.effective_go_config_hash`. Unreadable
configuration disables caching. The fingerprint identifies configuration
changes between runs. External tools and flag files can still change while
a test is running.

The sandbox digest covers the same entries as the module copy, including
embedded payloads, cgo inputs, test fixtures, symlink targets, modes, and
empty directories. The child environment is frozen during preparation, with
`GOWORK=off` for sandbox execution. Reports and cache keys use hashes of the
environment rather than raw values. A local `go.mod replace` or any
`GOFLAGS` setting with `-modfile`, `-overlay`, or `-toolexec` disables caching
and emits a warning. Use `--no-cache` when tests depend on arbitrary external
files that the sandbox digest cannot track.

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

[Subprocess cancellation limits](limitations.md#subprocess-cancellation) apply
to intentionally detached descendants. Preparation has a configurable
deadline and fixed subprocess output caps. See
[preparation resource limits](performance.md#r04--preparation-resource-limits).

The engine prepares the workspace and candidates, runs an uninstrumented
baseline, optionally profiles coverage, executes mutants, and constructs the
report. Cancellation during mutant execution produces a report with
`complete=false`.

## Diagnostic shape

Every result includes a verdict rule ID, precise source span, statement, source-diff/backend evidence, assumptions, and—only for survivors—a mutation-specific suggested test scenario.
