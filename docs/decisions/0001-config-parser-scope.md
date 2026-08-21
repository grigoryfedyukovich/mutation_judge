# ADR 0001: keep the strict flat config parser; do not adopt full TOML/YAML libraries

**Status:** Decided (2026-08-18). Closes the open question tracked in
`ISSUES.md` and referenced from `docs/limitations.md` and
`docs/spec-conformance.md`.

## Context

`internal/config` accepts a deliberately strict, flat subset of TOML/YAML
(`docs/spec-conformance.md`): one `key = value` (TOML) or `key: value`
(YAML) pair per line, scalars and single-level `[a, b, c]` lists only,
double/single-quoted strings with backslash escaping, `#` comments outside
quotes. Tables, nested maps, YAML anchors/aliases, multiline strings, and
YAML's native block-list syntax are all rejected with a diagnostic rather
than approximated. This was documented as an MVP compromise ("a
publication/distribution decision" left open) rather than a permanent
design choice. `mutation-judge` has zero external dependencies today (no
`require` block in `go.mod`, no `go.sum`).

## Decision

**Keep the strict flat subset as the permanent parser. Do not add a TOML
or YAML dependency.** Separately, extend the subset to accept YAML's
native block-list syntax for list-valued keys (implemented alongside this
decision — see below), since that is the one concrete, common piece of
idiomatic syntax the strict subset was rejecting.

## Reasoning

**The schema is, and is likely to remain, entirely flat.** Every field in
`Config` (`internal/config/config.go`) is a scalar or a single-level string
list; there is no nested structure anywhere in the 13 supported keys, and
nothing in the roadmap suggests one is coming (no per-operator settings,
profiles, or environment sections are proposed anywhere in `ISSUES.md` or
the docs). A full TOML/YAML library buys zero additional expressiveness
for the schema as it exists. If the schema ever does need real nesting
(e.g. a future `[operators.boundary]`-style per-operator config table),
that is the point to revisit this decision — not before.

**Zero dependencies is a load-bearing property for this specific tool, not
an incidental one.** `mutation-judge` parses and mutates a user's source
tree and executes `go test` against the result. A tool in that trust
position benefits unusually strongly from having no third-party code in
its dependency graph: no `go.sum` to audit, no transitive supply-chain
surface, and every line that runs to parse a config file is auditable in
the ~340 lines of `internal/config/config.go` in one sitting — which is
exactly what happened in the course of writing this decision. A general
TOML or YAML library is thousands of lines by comparison and would need to
be trusted wholesale instead.

**A "full" YAML parser is not obviously a determinism upgrade.** YAML 1.1
(what most Go YAML libraries still implement by default) auto-converts
bare `yes`/`no`/`on`/`off` to booleans and has had real, widely-documented
incidents from this (the "Norway problem": the string `NO`, e.g. a country
code, silently becoming `false`). Anchors and aliases let one part of a
document silently rewrite another. The project's own stated design
philosophy elsewhere (`TIMEOUT` as a first-class outcome rather than
folding into failure, unknown config keys erroring by default, `UNKNOWN`/
`UNSUPPORTED` never counted as a successful proof) is consistently "fail
loudly and specifically rather than silently approximate." The strict
subset's behavior — reject anything outside the documented grammar, with a
line number and a specific error — is more consistent with that
philosophy than adopting a full parser would be, independent of the
dependency question.

**The actual friction is narrow and fixable without a dependency.** In
practice the only place a user would hit the strict subset's limits with
the *current* schema is writing an `operators` list in YAML's native
block-list style:

```yaml
operators:
  - boundary
  - boolean
```

instead of the previously-required inline form (`operators: ["boundary",
"boolean"]`). That is unambiguous to parse for a single flat list value —
each line at one deeper indent than the key, starting with `- `, is
unarguably one list entry — so it does not require adopting general
nested-YAML parsing to support. This has been added to the parser (see
`internal/config/config.go`, `internal/config/config_test.go`) alongside
this decision. Genuine nested tables/maps remain rejected, since those
would require the general-parsing machinery this decision declines to
adopt.

## Consequences

- `docs/limitations.md`'s "Replace the strict flat configuration subset
  with full TOML/YAML libraries if dependency policy permits" is removed;
  this decision is that policy call, made.
- `docs/spec-conformance.md`'s "remains a publication/distribution
  decision" is updated to point here.
- Future contributors proposing a TOML/YAML dependency should treat this
  as the decision to revisit, not a blank slate — specifically, they
  should identify what nested schema need has actually arisen, since "more
  standard-compliant parsing" alone was considered and declined here.
