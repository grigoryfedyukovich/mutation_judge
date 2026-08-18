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
- `internal/gitdiff`: zero-context diff hunk parsing for changed-line mode.
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

## Sandbox lifecycle

One temporary module copy is created for an analysis. The baseline runs in that copy. Each mutant is then applied and restored serially. This is much cheaper than copying the repository for every mutant while preserving the one-mutant-at-a-time invariant.

The temporary copy excludes `.git`, `.mutation-judge`, and the configured cache path. General symlinks are preserved, but mutation targets are checked lexically and after symlink resolution; a source path that escapes the sandbox is rejected. Mutation and restoration use same-directory temporary files plus atomic rename and preserve the original file mode. Cleanup runs on every normal/error return from orchestration.

## Cache key

```text
SHA-256(
  CLI version,
  operator semantic version,
  source/module/test digest,
  effective configuration JSON,
  backend name and version,
  mutant stable ID,
  replacement
)
```

The stored JSON also has an independent schema marker. This prevents old result layouts from being silently accepted.

## Orchestration phases

`analysis.Engine` separates four phases: workspace/candidate preparation, baseline execution, serial mutant execution, and report construction. A cancellation after at least one completed mutant produces a report with `complete=false`; it is never mislabeled as a complete run.

## Diagnostic shape

Every result includes a verdict rule ID, precise source span, statement, source-diff/backend evidence, assumptions, and—only for survivors—a mutation-specific suggested test scenario.
