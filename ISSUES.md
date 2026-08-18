# Issue list

Resolved findings from the 2026-07-18 audit are recorded in [`docs/reviews/review-resolution-v0.1.2.md`](docs/reviews/review-resolution-v0.1.2.md).

## Correctness and trust work

- [ ] Replace heuristic compiler-output matching with structured `go test -json` event classification and explicit build diagnostics.
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
