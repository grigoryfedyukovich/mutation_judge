# Limitations and planned work

## v0.1 limitations

1. **Single Go module.** Package patterns must resolve inside the current module. Multi-module workspaces are not yet modeled as one mutation unit.
2. **Production Go files only.** `GoFiles` and `CgoFiles` returned by `go list` are candidates. Test files, generated files by default, assembly, templates, and generated-at-test-time code are excluded.
3. **Narrow fallback for classification.** `go test -json` event classification (a package-level `[build failed]` marker, combined with whether any test ever started) is authoritative for INVALID vs. KILLED. A regex-based fallback exists only for the rare case where `go test` rejects the invocation before writing any JSON at all (e.g. a build-constraint exclusion) — see `internal/runner.classifyEvents` and `docs/evaluation.md`/`ISSUES.md` for the change from the previous purely heuristic approach.
4. **Responsible tests depend on go test's own event stream.** Named tests are read from the `Test` field of `go test -json` events, including a test still "in flight" (started but never resolved) when the process crashed. A package-wide crash before any test starts (init() panic, `TestMain` exiting) still kills the mutant with no test name attributed, since none ran.
5. **One test selection for all mutants.** The MVP runs the package patterns supplied by the user. It does not yet map every mutant to a smaller package/test shard.
6. **Baseline statement coverage only.** Coverage does not identify assertions or dynamic dependencies and can be absent for packages unsupported by Go coverage.
7. **No equivalent-mutant proof.** A survivor may be equivalent under all reachable program states or merely untested. Mutation Judge does not claim to decide equivalence.
8. **Git changed-line semantics.** Only added/modified lines in the current file version are selectable. Deleted-only lines have no current AST span.
9. **Serial execution.** This deliberately preserves simple isolation. Progress lines are available, but distributed CI and parallel sandboxes are future work.
10. **Flat configuration subset.** The strict dependency-free parser supports the documented scalar/list keys, not the full TOML or YAML language; YAML's native block-list style is additionally accepted for list-valued keys. Other unsupported nested syntax is an error. This is a permanent design decision, not a placeholder — see `docs/decisions/0001-config-parser-scope.md`.
11. **Conservative I/O.** Every run hashes relevant module source and copies the module tree. This favors stale-cache prevention and sandbox fidelity over large-repository startup speed.

## Correctness priorities

None outstanding. The last item, a persistent journal for abnormal process termination, is now implemented: `cmd/mutation-judge` appends one NDJSON entry (timestamp, signal, phase, patterns, operators, and progress counts) to `.mutation-judge/journal.ndjson` on every SIGINT/SIGTERM, independent of `--no-cache`. See `ISSUES.md`.

The four operators formerly listed under "Optional expansion" as future work -- error-return, switch-case deletion, loop-bound changes, and channel/select behavior -- are now implemented (`errorreturn`, `switch`, `loop`, `channel`; all opt-in, none in `Default()`). See `docs/semantics.md` for what each one matches and, for `loop` and `channel`, what each deliberately does *not* mutate to avoid producing slow, uninformative `TIMEOUT` verdicts.

## Optional expansion

- Mutant-to-package and mutant-to-test selection using coverage.
- Parallel isolated workers and distributed CI manifests.
- Assertion/contract attribution beyond failing test names.
- GitHub annotations and SARIF output.
- Cross-run HTML comparison and trend visualization.
