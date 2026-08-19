# Issue list

Resolved findings from the 2026-07-18 audit are recorded in [`docs/reviews/review-resolution-v0.1.2.md`](docs/reviews/review-resolution-v0.1.2.md).

## Correctness and trust work

- [x] Replace heuristic compiler-output matching with structured `go test -json` event classification and explicit build diagnostics. `internal/runner.GoTest` now classifies from the `-json` event stream: a package-level `[build failed]` marker (the tool's own FAIL-summary line, emitted identically for compile *and* vet failures) means INVALID; a package that fails before any test starts for a non-build reason (init() panic, `TestMain` calling `os.Exit`, ...) means KILLED with no test attributed; a test still "started" when the stream ends (an unrecovered panic in a goroutine the testing package doesn't supervise, which aborts the binary without a clean per-test fail event) is attributed as responsible. The previous English-substring regex is kept only as a last-resort fallback for the rare case where `go test` rejects the invocation before `test2json` ever writes JSON (e.g. build constraints exclude all Go files). This also fixed a latent attribution bug: the old `--- FAIL: ` line regex was anchored to line-start and silently missed every subtest name, since Go indents nested subtest result lines (`    --- FAIL: TestX/sub`) — a common table-driven-test pattern. "killed by" now correctly includes subtests.
- [ ] Add process-signal tests proving temporary-workspace cleanup under abrupt CLI termination.
- [ ] Add a fixed external corpus evaluation with manually reviewed survivors.
- [ ] Test cgo, build tags, package initialization failures, custom `TestMain`, and unusual toolchain failures.
- [ ] Decide whether publication builds should use full TOML/YAML libraries instead of the documented strict flat subset.
- [ ] Add an explicit warning/evidence field for non-fatal cache write failures.

## Performance work

- [ ] Measure source-tree digest and full-sandbox copy cost on large modules before narrowing either correctness boundary.
- [ ] Explore copy-on-write/reflink sandbox creation where the host filesystem supports it.
- [ ] Add coverage-guided package/test sharding.
- [ ] Add parallel isolated workers with deterministic output ordering.

## Optional feature expansion

- [ ] Distributed CI manifests and workers.
- [ ] SARIF and GitHub annotation rendering.
- [ ] Additional targeted operators for error returns, switch cases, loops, and channels.
- [ ] Cross-run HTML comparisons and trend reports.
- [ ] Conservative equivalent-mutant suppression for locally provable guarded comparisons.
