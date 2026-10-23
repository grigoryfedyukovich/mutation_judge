# Integration-suite reliability

The integration package builds the mutation-judge CLI **once** per test process and
reuses that executable. `TestMain` removes the private build directory after
all tests finish; tests may use `t.Parallel` without racing independent builds.
Commands started by this package have an outer 120-second deadline and a
finite one-second output-pipe cleanup grace. These are test-harness safeguards
only; CLI command timeouts are tested separately.

To check the entire repository without Go package build contention:

```sh
go test -p 1 ./... -count=1 -timeout=240s
```

For diagnostic detail while inspecting integration behavior:

```sh
go test ./tests/integration -count=1 -v -timeout=180s
```

Coverage/package-selection tests no longer assert on elapsed wall-clock time.
Instead they inspect each mutant's recorded `diagnostic.evidence.test_command`
and its verdict/responsible test. Keep at least one correctness check using a
real test process; command text alone does not prove the test was executed.

A global `go test` timeout and a fixture subprocess deadline serve different
purposes. A slow or canceled command should fail visibly rather than make an
entire package appear indefinitely stalled.
