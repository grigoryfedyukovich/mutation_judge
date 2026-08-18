# `mutation-judge` — Thorough Code Review

> Reviewed: 2026-07-18 | Go 1.22 module | ~1,600 source LOC across 12 non-test files

---

## 1. Extracted Specification

### What the tool does
`mutation-judge` is a **targeted Go mutation-testing CLI**. Given a set of Go package patterns it:

1. Discovers source files inside the current Go module.
2. Applies AST-based source-span replacements (mutations) to a temporary sandbox copy of the module.
3. For each mutant, runs `go test` and classifies the result.
4. Emits a scored report in text, JSON, or HTML.
5. Optionally enforces a CI pass/fail policy via a minimum score threshold.

### Mutation operators
| Operator | Rule ID | Transformation |
|---|---|---|
| `boundary` | `MJ-BOUNDARY` | `<`↔`<=`, `>`↔`>=` |
| `boolean` | `MJ-BOOL-DROP-RIGHT` | Drop right operand of `&&`/`\|\|` |
| `boolean` | `MJ-BOOL-DROP-LEFT` | Drop left operand of `&&`/`\|\|` |
| `boolean` | `MJ-BOOL-DROP-NOT` | Remove `!` negation |
| `boolean` | `MJ-BOOL-LITERAL` | Flip `true`/`false` literal |
| `arithmetic` | `MJ-ARITHMETIC` | `+`↔`-`, `*`↔`/` |

### Verdict taxonomy
`KILLED` · `SURVIVED` · `INVALID` · `TIMEOUT` · `UNKNOWN` · `UNSUPPORTED`

**Score formula:** `100 × killed / (killed + survived)` — INVALID and TIMEOUT are excluded from the denominator.

### Config keys (TOML/YAML flat)
`operators` · `timeout` · `test_run` · `format` · `output` · `cache_dir` · `cache` · `max_mutants` · `ci_min_score` · `ci_exit_code` · `include_generated` · `changed`

### Execution model
- One module copy per analysis run (not per mutant).
- Mutations applied and restored serially, one at a time.
- Results cached by a SHA-256 key covering tool version, Go version, source digest, config JSON, mutant ID, and replacement text.
- `--changed <git-ref>` mode limits mutations to lines modified relative to a Git revision.

---

## 2. Bugs

### B-1 · Double render wastes work and mis-measures timing `[main.go:118–143]`
The report is **rendered twice**: once to a `bytes.Buffer` called `probe` (to time it), and once to the real destination. The probe result is silently discarded. The second render's elapsed time is **not captured** — so `RenderingMS` only reflects the cost of the discarded render, not the one that actually reaches the user.

**Fix:** Render once into a `bytes.Buffer`, record elapsed time, patch `r.Timing`, then `io.Copy` to the destination. This eliminates the double work and measures the actual render.

```go
var buf bytes.Buffer
renderStart := time.Now()
if err := report.Render(&buf, cfg.Format, r); err != nil { ... }
r.Timing.RenderingMS = time.Since(renderStart).Milliseconds()
r.Timing.TotalMS += r.Timing.RenderingMS
if _, err := io.Copy(dst, &buf); err != nil { ... }
```

---

### B-2 · `trimOutput` slices mid-rune on multi-byte UTF-8 `[runner/runner.go:80]`
```go
return fmt.Sprintf("...", strings.TrimSpace(s[:max]), len(s))
```
`s[:max]` is a byte-count slice, not a rune-count slice. If the 65 536th byte falls inside a multi-byte UTF-8 rune the resulting string is invalid UTF-8 and downstream JSON encoding will replace affected bytes with the Unicode replacement character.

**Fix:**
```go
for !utf8.ValidString(s[:max]) { max-- }
```

---

### B-3 · `stripComment` mishandles mixed quote styles `[config/config.go:113]`
```go
if r == '"' || r == '\'' {
    quoted = !quoted
}
```
A single toggle tracks both `"` and `'` together. The string `"it's fine # or not"` would set `quoted=true` on `"`, flip it back to `false` on `'`, then treat `#` as a comment start. Any TOML value containing an apostrophe inside double quotes (e.g. `test_run = "TestIt's"`) would be silently truncated.

**Fix:** Track the opening quote character and only toggle on the matching closing character.

---

### B-4 · `Apply` can leave sandbox corrupted if write fails `[workspace/workspace.go:120]`
```go
original, err := os.ReadFile(path)   // ok
...
if err := os.WriteFile(path, mutated, 0o644); err != nil {
    return nil, err                   // restore func is NOT returned
}
```
If `WriteFile` fails after partial write (e.g. disk full), the file is corrupted and no restore function is returned to the caller. Subsequent mutants applied to the same file — or even the same analysis baseline — would operate on corrupt source.

**Fix:** Write to a side-file (`path + ".orig"` backup), or write the mutated version to a `.tmp` file and rename atomically; restore by renaming the backup back.

---

### B-5 · `cfgJSON` marshaling error silently dropped `[analysis/analysis.go:101]`
```go
cfgJSON, _ := json.Marshal(req.Config.AsMap())
```
If `json.Marshal` returns an error, `cfgJSON` is `nil`. Every mutant's cache key is then computed from an empty configuration JSON, meaning **all mutants share a cache namespace regardless of config differences**. A config change (e.g. different `test_run`) would return stale cached results.

**Fix:** Propagate the error; config marshaling should never fail in practice but the invariant must be enforced.

---

### B-6 · `report.Complete` is always hardcoded `true` `[analysis/analysis.go:135]`
When the context is cancelled mid-run the function returns an error, so no partial report is produced. But if future refactoring returns a partial report (e.g. for a `--partial` mode), `Complete: true` would be a lie. The field exists in the model schema for a reason — it should reflect `ctx.Err() == nil`.

---

### B-7 · `arithmetic` MUL→DIV mutant causes runtime panic, not INVALID `[frontend/frontend.go:149]`
The arithmetic operator maps `*` → `/`. If the right-hand operand is zero at runtime the program panics. The `buildRE` heuristic in `runner.go` does not match panic output, so the verdict would be **KILLED** rather than **INVALID**. While KILLED is not wrong (the test "fails"), panics from instrumentation rather than logic are noise in the report and can obscure the real reason a mutant was killed.

---

### B-8 · `renderText` ignores all `fmt.Fprintf` errors after the first `[report/report.go:50–64]`
Only the first `fmt.Fprintf` call captures `err`. The remaining seven calls silently ignore write errors. If the writer is a file and the disk fills up mid-render, the report is truncated with no error returned to the caller.

**Fix:** Use a `bufio.Writer`-style wrapper that latches the first error, or check each return.

---

### B-9 · Concurrent use of the same `.tmp` cache path `[cache/cache.go:64]`
```go
tmp := filepath.Join(s.Dir, key+".tmp")
```
If two `mutation-judge` processes run simultaneously against the same cache directory with the same key (possible in parallel CI matrix jobs on shared storage), both write to the identical `.tmp` path and race. The final `os.Rename` is atomic on POSIX, but the concurrent `os.WriteFile` calls to the same path are not.

**Fix:** Include the process ID (or a random suffix) in the temp filename:
```go
tmp := filepath.Join(s.Dir, fmt.Sprintf("%s.%d.tmp", key, os.Getpid()))
```

---

## 3. Code Smells

### S-1 · `Analyze` is a 150-line orchestration monolith `[analysis/analysis.go]`
The function handles: workspace validation, package listing, file enumeration, git diff loading, mutation discovery, sandbox creation, baseline execution, coverage parsing, cache resolution, mutant execution and restore, result building, and report assembly. Any single concern is entangled with all others. It violates single-responsibility and is difficult to test incrementally.

**Suggested split:** `prepareWorkspace()`, `runBaseline()`, `executeMutants()`, `buildReport()`.

---

### S-2 · Hand-rolled TOML/YAML parser that isn't TOML or YAML `[config/config.go]`
The parser splits on `=` or `:` depending on file extension and supports only flat scalar/list keys. Users familiar with TOML or YAML will expect comments, inline tables, multi-line strings, or anchors — none of which work and some of which silently parse wrong (see B-3). Calling it "TOML/YAML" is misleading.

**Options:** Use `github.com/BurntSushi/toml` + `gopkg.in/yaml.v3` (two small dependencies), or rename the format and clearly document it as a custom flat key=value dialect.

---

### S-3 · `samePath` does two `filepath.Abs` calls on every WalkDir entry `[workspace/workspace.go:95,195]`
```go
samePath(path, cacheAbs)   // called for EVERY directory entry
```
`filepath.Abs(cacheAbs)` is recomputed on every call but `cacheAbs` is invariant across the walk. For a module with thousands of files this adds unnecessary syscalls.

**Fix:** Pre-compute `abs(cacheAbs)` once before `WalkDir`.

---

### S-4 · Inline HTML template as a single minified string `[report/report.go:66]`
The HTML template is an unreadable one-liner embedded in source. It is impossible to validate, lint, or extend without reformatting. Go 1.16+ `//go:embed` makes embedding a real file trivial and keeps the template maintainable.

---

### S-5 · `SortedResponsible` is exported for no apparent consumer `[analysis/analysis.go:165]`
The function is unreferenced outside the package. Exporting it adds public API surface that must be maintained. Either consume it from the `report` package or unexport it.

---

### S-6 · `mutationID` uses SHA-256 (a cryptographic hash) for a stable ID `[frontend/frontend.go:191]`
SHA-256 is correct but over-engineered for a non-security purpose. FNV-1a or xxHash is faster for this use case (short-lived IDs, no adversarial input) while producing equally stable 12-character hex IDs.

---

### S-7 · `dedupe` is O(n) map traversal after generation that should not produce duplicates `[frontend/frontend.go:202]`
The comment-free deduplication pass at the end of `discoverFile` suggests the generation logic may occasionally produce duplicates, but there is no documentation or test explaining when that happens. Either document the case (e.g. same span from two operators) or assert it cannot happen and remove the pass.

---

### S-8 · `VerdictUnknown` is a reachable default with no upstream producer `[analysis/analysis.go:161, runner/runner.go]`
The `GoTest` backend never emits `VerdictUnknown` or `VerdictUnsupported`. The `summarize` function counts both but `Unknown` would only appear via a custom `Backend` implementation. The `Unknown` path should either be removed from the public model or documented as the extension point for third-party backends.

---

### S-9 · `testCommand` in `analysis.go` does not include `--timeout` flag `[analysis/analysis.go:177]`
The `Bounds.test_command` field shown in reports does not include `-timeout`, so users cannot directly copy-paste it to reproduce the exact mutation test invocation. Minor but misleading.

---

### S-10 · `cleanOutputDir` is misnamed `[main.go:175]`
The function extracts the parent directory of an output file path. Naming it `cleanOutputDir` implies it sanitizes a directory path, not that it derives a directory from a file path. Rename to `parentDir` or `outputDir`.

---

## 4. Performance Bottlenecks

### P-1 · Serial mutation execution (by design, but worth quantifying)
The dominant cost is serial subprocess invocation: one `go test` per mutant. A project with 500 mutants and a 3-second test suite = **25 minutes**. The architecture explicitly defers parallelism (ISSUES.md), but there is no progress output during execution — users see nothing until the entire run completes.

**Quick win (no parallelism required):** Emit a progress line per mutant: `[42/500] M-abc123 boundary pkg/foo.go:32`.

---

### P-2 · `workspace.Digest()` reads every Go file on every run `[workspace/workspace.go:147]`
`Digest` walks and hashes the full source tree to produce a cache key. On a large module this can read megabytes of source just to compute a digest that then invalidates all cached results on any file change (correct but expensive). The digest is the outer cache key; inner keys already include the mutant ID and replacement, so the source digest is mostly an invalidation signal.

**Consideration:** Only include `go.mod` + `go.sum` in the digest for dependency changes, plus hashes of the specific files containing the discovered mutations.

---

### P-3 · `workspace.CopyModule()` copies the full module tree every run `[workspace/workspace.go:74]`
The sandbox is a full copy of the module. For modules with large `vendor/` directories or large binary test fixtures this is an expensive I/O operation done unconditionally.

**Partial mitigation:** Skip `vendor/` only if the module uses `-mod=mod` or doesn't vendoring; or mount files via OS-level copy-on-write if available.

---

### P-4 · `coverage.Covered` O(n) linear fallback scan `[coverage/coverage.go:79]`
```go
for k, v := range m.Lines {
    if strings.HasSuffix(k, "/"+file) || k == file {
```
For every mutant, if the exact file key doesn't match, the code scans all keys looking for a suffix match. With 200 coverage entries and 500 mutants this is 100,000 string operations — avoidable with a pre-built index.

**Fix:** Build a `map[string]string` from filename-suffix to full key at parse time.

---

### P-5 · `unifiedDiff` re-scans the full file source per mutant `[frontend/frontend.go:212]`
Three separate `bytes.LastIndex` / `bytes.Index` / `bytes.Count` calls walk the entire file byte-slice for every mutation candidate. For a 5,000-line file with 30 mutants this scans ~150,000 lines worth of bytes. Pre-building a line-offset index (slice of byte offsets per line) per file would reduce each call to an O(1) lookup.

---

### P-6 · `go list -json -e` output is decoded by a streaming `json.NewDecoder` but the entire output was already buffered `[workspace/decode.go]`
```go
out, err := cmd.Output()   // full buffer
return decodePackages(out) // then streams it again
```
The output is already fully buffered by `cmd.Output()`. Using a streaming decoder adds object overhead without benefit. This is minor but inconsistent.

---

## 5. Security Observations

### SE-1 · `workspace.Apply` does not validate that `rel` stays within sandbox root
```go
path := filepath.Join(root, filepath.FromSlash(rel))
```
There is no check that the resulting `path` is inside `root`. The caller derives `rel` from `mut.Span.File` which originates from `frontend.Discover`, which receives files vetted by `workspace.SourceFiles` (which does check for `..` prefixes). However, the validation gap inside `Apply` itself means any future caller could inject a path escape. Add a defensive check:
```go
if !strings.HasPrefix(path, root+string(filepath.Separator)) {
    return nil, fmt.Errorf("path escape: %s", rel)
}
```

### SE-2 · `git diff` pathspec `*.go` behavior is non-obvious `[gitdiff/gitdiff.go:19]`
```go
exec.Command("git", "diff", "--unified=0", "--no-ext-diff", base, "--", "*.go")
```
Git pathspecs are recursive by default (unlike shell globs), so `*.go` correctly matches `internal/foo.go`. However, this is not obvious to future maintainers and the behavior differs from shell expectations. Use `**/*.go` or a top-level `:/*.go` magic pathspec for clarity, and add a comment.

---

## 6. Test Coverage Gaps

| Area | Gap |
|---|---|
| `cache.Store` | No test for `Put`→`Get` round-trip, schema mismatch rejection, or disabled-cache no-op |
| `workspace.Apply` | No test for partial write failure, path-escape attempt, or zero-length span |
| `coverage.Parse` | No test for the suffix-match fallback path in `Covered` |
| `runner.GoTest` | No test for `VerdictTimeout` or the `buildRE` patterns |
| `config.Load` | No test for apostrophe-in-double-quote (the B-3 bug) |
| `gitdiff.Parse` | Only one happy-path fixture; no test for deleted files (`/dev/null`), zero-count hunks, or binary files |
| `report.renderHTML` | No test at all |
| `analysis.Analyze` | No test for context cancellation, `MaxMutants` truncation, or CI score policy |

The `runner_test.go` contains exactly **one** test function covering `failingTests`. The entire `GoTest.Run` subprocess path has no test coverage.

---

## 7. Summary Table

| # | Severity | Category | File | Short Description |
|---|---|---|---|---|
| B-1 | High | Bug | `main.go` | Double render; second render time uncaptured |
| B-2 | High | Bug | `runner/runner.go` | `trimOutput` slices mid-rune |
| B-3 | Medium | Bug | `config/config.go` | `stripComment` breaks on mixed quote styles |
| B-4 | Medium | Bug | `workspace/workspace.go` | `Apply` leaves corrupted file if write fails |
| B-5 | Medium | Bug | `analysis/analysis.go` | `cfgJSON` error silently dropped → cache collisions |
| B-6 | Low | Bug | `analysis/analysis.go` | `Complete: true` hardcoded |
| B-7 | Low | Bug | `frontend/frontend.go` | Arithmetic `*`→`/` panic classified as KILLED |
| B-8 | Medium | Bug | `report/report.go` | `renderText` swallows write errors |
| B-9 | Low | Bug | `cache/cache.go` | Concurrent `.tmp` write race |
| S-1 | High | Smell | `analysis/analysis.go` | Monolithic 150-line `Analyze` function |
| S-2 | High | Smell | `config/config.go` | Fake TOML/YAML parser |
| S-3 | Medium | Smell | `workspace/workspace.go` | `samePath` recomputes abs on every walk step |
| S-4 | Medium | Smell | `report/report.go` | HTML template inline unreadable string |
| S-5 | Low | Smell | `analysis/analysis.go` | `SortedResponsible` exported unnecessarily |
| S-6 | Low | Smell | `frontend/frontend.go` | SHA-256 for a non-security ID |
| S-7 | Low | Smell | `frontend/frontend.go` | Unexplained `dedupe` pass |
| S-8 | Low | Smell | `model/model.go` | `VerdictUnknown` unreachable from built-in backend |
| S-9 | Low | Smell | `analysis/analysis.go` | `testCommand` missing `-timeout` flag |
| S-10 | Low | Smell | `main.go` | `cleanOutputDir` misleading name |
| P-1 | High | Perf | `analysis/analysis.go` | No progress output during serial run |
| P-2 | Medium | Perf | `workspace/workspace.go` | Full source tree digest on every run |
| P-3 | Medium | Perf | `workspace/workspace.go` | Full module copy on every run |
| P-4 | Medium | Perf | `coverage/coverage.go` | O(n) suffix scan per mutant |
| P-5 | Low | Perf | `frontend/frontend.go` | `unifiedDiff` re-scans full file per mutant |
| P-6 | Low | Perf | `workspace/decode.go` | Streaming decoder on already-buffered output |
| SE-1 | Medium | Security | `workspace/workspace.go` | `Apply` lacks path-escape guard |
| SE-2 | Low | Security | `gitdiff/gitdiff.go` | `*.go` pathspec non-obvious semantics |
