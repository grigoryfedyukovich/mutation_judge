#!/usr/bin/env bash
# Repeatable private-spool throughput and peak-RSS measurements. Not a full
# mutation-analysis benchmark: no Go packages are mutated or executed here.
set -euo pipefail
cd "$(dirname "$0")/.."
fixture_bin="$(mktemp -d)/mutation-judge-s15-spool.test"
trap 'rm -rf "$(dirname "$fixture_bin")"' EXIT

go test -c -o "$fixture_bin" ./internal/mutantspool
printf '\n--- Decode-only benchmark (32,768 disk-backed mutations) ---\n'
"$fixture_bin" -test.run '^$' -test.bench '^BenchmarkR07S15FinalCompactDecode/records-32768$' -test.benchmem -test.count=3 -test.benchtime=500ms
printf '\n--- 131,072 mutation spool records, two complete reads ---\n'
if [[ "$(uname -s)" == "Darwin" ]]; then
    /usr/bin/time -l env GOGC=100 MUTATION_JUDGE_R07_S15_RSS=1 "$fixture_bin" -test.run '^TestR07S15LargeSpoolRSSFixture$' -test.v
elif command -v /usr/bin/time >/dev/null 2>&1; then
    /usr/bin/time -v env GOGC=100 MUTATION_JUDGE_R07_S15_RSS=1 "$fixture_bin" -test.run '^TestR07S15LargeSpoolRSSFixture$' -test.v
else
    env GOGC=100 MUTATION_JUDGE_R07_S15_RSS=1 "$fixture_bin" -test.run '^TestR07S15LargeSpoolRSSFixture$' -test.v
fi
