# Resolution of the 2026-07-18 code review

The source review is preserved as [`code-review-2026-07-18.md`](code-review-2026-07-18.md). This note records what changed in v0.1.2 and where the implementation deliberately differs from a recommendation.

## Bugs and security findings

| Finding | Resolution |
|---|---|
| B-1 double render | Fixed. `report.RenderMeasured` serializes once, measures that serialization, patches reserved timing fields, and emits the finished bytes. |
| B-2 UTF-8 truncation | Fixed. Truncation backs up to a valid UTF-8 boundary. |
| B-3 mixed quote comments | Fixed. The parser tracks the actual opening quote and double-quote escapes. Regression covers `"TestIt's # Fine"`. |
| B-4 failed writes can corrupt sandbox | Fixed. Mutation and restoration use same-directory temporary files and atomic rename while preserving mode. A forced rename-failure regression proves the destination remains unchanged. |
| B-5 ignored config JSON error | Fixed. Marshaling errors are propagated before cache lookup. |
| B-6 hard-coded completeness | Fixed. Cancellation after a completed mutant returns a partial report with `complete=false`; partial output is therefore explicitly labeled. |
| B-7 multiplication-to-division panic | Recommendation not applied. A runtime panic under a valid mutant is a semantic test failure and therefore `KILLED`, not `INVALID`. Documentation and tests now state this explicitly. |
| B-8 ignored text write errors | Fixed with a first-error-latching writer and a late-failure regression. |
| B-9 shared cache temp path | Fixed with per-write unique temporary files; concurrent same-key writes are tested. |
| SE-1 path escape | Fixed. Apply validates lexical and resolved paths, rejects escapes and source symlinks outside the sandbox, and tests both cases. |
| SE-2 Git pathspec clarity | Fixed with an explicit Git glob-magic pathspec and comment. Deleted-file parsing was also hardened. |

## Design and maintainability

- **S-1:** `Analyze` is split into preparation, baseline, mutant execution, and report construction.
- **S-2:** The parser remains dependency-free, but is now explicitly documented as a strict flat TOML/YAML subset and rejects unsupported structures. This avoids claiming silent full-language support.
- **S-3:** The cache directory absolute path is computed once per copy operation.
- **S-4:** HTML moved to an embedded, formatted `report.html.tmpl` file.
- **S-5:** The unused exported `SortedResponsible` helper was removed.
- **S-6:** SHA-256 IDs were retained. Candidate strings are tiny, hashing is not measurable beside subprocess execution, and collision-resistant stable IDs are useful in persisted reports.
- **S-7:** The unexplained deduplication pass was removed; duplicate IDs now trigger an internal invariant error.
- **S-8:** `UNKNOWN` and `UNSUPPORTED` were retained because the product specification requires them as first-class backend outcomes. The built-in backend now emits `UNKNOWN` for process-start/cancellation cases.
- **S-9:** Reproduction commands now include `-timeout` and match the concrete Go invocation.
- **S-10:** `cleanOutputDir` was renamed to `parentDir`.

## Performance

- **P-1:** Optional per-mutant progress is emitted to stderr and can be disabled with `--progress=false`.
- **P-4:** Coverage suffix resolution is pre-indexed, with ambiguous suffixes rejected.
- **P-5:** Each source file now builds one line-offset index used by all unified-diff generation.
- **P-2/P-3:** Full source hashing and full module copying remain conservative correctness choices. Narrow hashing and copy-on-write sandboxes are tracked as future optimizations because careless narrowing can reuse stale test results or bypass vendored/test fixtures.
- **P-6:** Buffered `go list` decoding remains; its cost is negligible relative to `go list` and `go test`, and changing it would not reduce peak buffering without restructuring process I/O.

## Added regression coverage

The release adds cache round trips/schema rejection/concurrency, atomic apply and path defenses, UTF-8 truncation, timeout/build/panic backend classification, mixed-quote configuration, coverage suffix lookup, deleted and zero-count diff hunks, HTML escaping, writer failures, measured rendering, max-mutant reporting, cancellation completeness, backend identity, and exact timeout command tests.
