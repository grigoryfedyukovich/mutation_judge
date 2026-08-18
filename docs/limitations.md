# Limitations and planned work

## v0.1 limitations

1. **Single Go module.** Package patterns must resolve inside the current module. Multi-module workspaces are not yet modeled as one mutation unit.
2. **Production Go files only.** `GoFiles` and `CgoFiles` returned by `go list` are candidates. Test files, generated files by default, assembly, templates, and generated-at-test-time code are excluded.
3. **Heuristic invalid classification.** The backend recognizes common Go compiler/type-check diagnostics. An unusual tool failure can be classified as killed when it is operational rather than test-semantic.
4. **Responsible tests depend on Go output.** Named tests are extracted from `--- FAIL:` lines. Package initialization failures or process-wide crashes may kill a mutant without a responsible test name.
5. **One test selection for all mutants.** The MVP runs the package patterns supplied by the user. It does not yet map every mutant to a smaller package/test shard.
6. **Baseline statement coverage only.** Coverage does not identify assertions or dynamic dependencies and can be absent for packages unsupported by Go coverage.
7. **No equivalent-mutant proof.** A survivor may be equivalent under all reachable program states or merely untested. Mutation Judge does not claim to decide equivalence.
8. **Git changed-line semantics.** Only added/modified lines in the current file version are selectable. Deleted-only lines have no current AST span.
9. **Serial execution.** This deliberately preserves simple isolation. Progress lines are available, but distributed CI and parallel sandboxes are future work.
10. **Flat configuration subset.** The strict dependency-free parser supports the documented scalar/list keys, not the full TOML or YAML language. Unsupported nested syntax is an error.
11. **Conservative I/O.** Every run hashes relevant module source and copies the module tree. This favors stale-cache prevention and sandbox fidelity over large-repository startup speed.

## Correctness priorities

- Replace output-pattern invalid detection with structured `go test -json` plus explicit build diagnostics.
- Add signal-aware cleanup tests and a journal for abnormal process termination.
- Add differential fixtures comparing source replacements with manually authored mutants.
- Expand precedence/parenthesization regression tests for complex boolean expressions.
- Replace the strict flat configuration subset with full TOML/YAML libraries if dependency policy permits.

## Optional expansion

- Mutant-to-package and mutant-to-test selection using coverage.
- Parallel isolated workers and distributed CI manifests.
- Assertion/contract attribution beyond failing test names.
- Additional carefully scoped operators: error-return removal, switch-case deletion, channel/select behavior, and loop-bound changes.
- GitHub annotations and SARIF output.
- Cross-run HTML comparison and trend visualization.
