# Limitations

## Current limitations

1. **Single Go module.** Package patterns must resolve inside the current module. A multi-module `go.work` (more than one `use` path) is rejected at startup with an error asking for `GOWORK=off` or a single-module workspace; a one-module workspace is allowed.
2. **Production Go files only.** `GoFiles` and `CgoFiles` returned by `go list` are candidates. Test files, generated files by default, assembly, templates, and generated-at-test-time code are excluded.
3. **Narrow fallback for classification.** `go test -json` event classification (a package-level `FAIL <pkg> [<reason>]` marker, combined with whether any test of that package ever started) is authoritative for INVALID vs. KILLED. A regex-based fallback exists only for the rare case where `go test` rejects the invocation before writing any JSON at all (e.g. a build-constraint exclusion) — see `internal/runner.classifyEvents` and `ISSUES.md`.
4. **Responsible tests, assertions and crashes depend on go test's own event stream.** `responsible_panics` records the first unindented `panic: ` or `fatal error: ` line attributed to a failed test, and only when the same test's output also has the runtime's `goroutine N [...]:` header; a crash with no attributable test (package init, output with no `Test` field) has none and stays test-only. Named tests are read from the `Test` field of `go test -json` events, including a test still "in flight" (started but never resolved) when the process crashed. Assertion text is additionally taken from that same stream's test-scoped `Output` events matching the testing package's indented `file.go:N:` log format, attached as `responsible_assertions`. `t.Log` on a failing test is indistinguishable from `t.Error` in the event stream and may appear there. A failing test with no such log (panic, `FailNow`, package init) stays test-only — the tool never guesses an assertion from the test source. A package-wide crash before any test starts (init() panic, `TestMain` exiting) still kills the mutant with no test or assertion attributed, since none ran.
5. **Test selection is based on the requested package patterns.** Opt-in package narrowing uses the test dependency graph. Opt-in per-test narrowing profiles ordinary top-level tests individually and preserves runnable examples and fuzz seeds. Uncertain scope or coverage falls back to the broader test command. See [test selection](performance.md#coverage-attributed-per-test-selection---coverage-test-selection).
6. **Baseline statement coverage only.** Coverage does not identify assertions or dynamic dependencies and can be absent for packages unsupported by Go coverage.
7. **Equivalence is decided only for three locally provable cases.** The frontend recognizes guarded comparisons, immediately overwritten boolean initializers, and pure return values discarded at every modeled call site. See [the proof conditions](semantics.md#conservative-equivalent-mutant-suppression).
8. **Git changed-line semantics.** Added/modified tracked lines and all lines of non-ignored untracked `.go` files are selectable. Deleted-only lines have no current AST span. A file ignored by Git or absent from the selected `go list` production package universe is not selected. Git selection uses captured source bytes and a pinned index. Concurrent edits can still exceed the [snapshot consistency guarantees](architecture.md#r03-consistent-preparation-snapshot).
9. **In-process parallelism only.** `--workers N` (opt-in, default 1) runs N mutants concurrently, each in its own sandbox, with deterministic report ordering. Distributed CI -- splitting one run's mutants across separate jobs or machines, then merging reports -- is not implemented. See `docs/performance.md`.
10. **Flat configuration subset.** The strict dependency-free parser supports the documented scalar/list keys, not the full TOML or YAML language; YAML's native block-list style is additionally accepted for list-valued keys. Other unsupported nested syntax is an error. This is a permanent design decision, not a placeholder — see `docs/decisions/0001-config-parser-scope.md`.
11. **Conservative I/O.** Every run hashes the same file set `CopyModule` places in the sandbox and copies the module tree (Linux may reflink per file via `FICLONE`, then fall back to a full copy). Local `go.mod replace` module paths are resolved and copied into the module snapshot; the temporary sandbox go.mod is rewritten to those copied targets, and a fresh copy is used for each test execution. Caching remains disabled for local replaces. Replacement trees that cannot be safely copied cause an explicit error. Outbound symlinks (targets outside the module root) are omitted; absolute in-tree symlinks are rewritten to sandbox-relative targets on copy. `node_modules/`, non-Go root `bin/`, and module-root `vendor/` without `modules.txt` are skipped. Real Go vendor trees are always included.
12. **Mutant IDs are byte-offset identity, not AST identity.** A mutant's ID hashes its file path and raw byte offset in that file (plus operator, rule, and replacement text) -- not a structural identity that survives unrelated edits. `compare` (see `docs/tutorial.md` section 16) relies on this ID to match mutants across two reports; an edit anywhere earlier in a file shifts the byte offset, and so the ID, of every later mutant in that file, even ones whose own code never changed. Comparisons across files a change never touches are unaffected. `compare` additionally runs a conservative, non-destructive correlation pass (`internal/compare.findLikelyShifts`) that recognizes an unambiguous fingerprint match (same file, operator, rule, original/replacement text, column span, and pre-mutation source line) between a removed entry and a brand-new-ID entry. It records the correlation in `likely_shifted` alongside the exact-ID verdict buckets.

## Comparison and history

`compare` requires complete runs that retained all discovered mutants,
without incremental filtering and with compatible execution provenance.
An older report may be valid JSON and still lack the required provenance.
Use the [comparison walkthrough](tutorial.md#comparison-compatibility-f09)
for exploratory comparisons and CI policy. History entries preserve
completeness, bounds, and test scope alongside the score.

## Subprocess cancellation

Supported Unix systems terminate the Go or Git subprocess group, including
ordinary descendants. A child that deliberately detaches can outlive
cancellation. Windows assigns the suspended subprocess to a kill-on-close
Job Object before execution and returns an error if assignment fails.
Other platforms cancel the direct child. See
[subprocess lifetime](architecture.md#f07-subprocess-lifetime-and-output-pipe-cleanup)
and [preparation resource limits](performance.md#r04--preparation-resource-limits).

Ongoing work is listed in the [issue backlog](../ISSUES.md).
