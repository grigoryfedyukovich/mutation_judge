#!/usr/bin/env bash
# Reproducible end-to-end RSS and throughput check for streaming mutation analysis.
# Usage: bash scripts/benchmark-r07-s7.sh /absolute/path/to/mutation-judge [functions=64] [workers=4] [output-dir]
set -euo pipefail
[[ $# -ge 1 ]] || { echo "usage: $0 /path/to/mutation-judge [functions] [workers] [output-dir]" >&2; exit 2; }
cli=$1
functions=${2:-64}
workers=${3:-4}
[[ "$functions" =~ ^[1-9][0-9]*$ && "$workers" =~ ^[1-9][0-9]*$ ]] || { echo 'functions and workers must be positive integers' >&2; exit 2; }
[[ -x "$cli" ]] || { echo "not executable: $cli" >&2; exit 2; }
cli=$(cd "$(dirname "$cli")" && pwd)/$(basename "$cli")
if [[ $# -ge 4 ]]; then
  output=$4
  mkdir -p "$output"
else
  output=$(mktemp -d "${TMPDIR:-/tmp}/mutation-judge-r07-s7.XXXXXXXX")
fi
output=$(cd "$output" && pwd)
project="$output/project"
mkdir -p "$project"
printf 'module example.test/mutation-judge-r07s7-benchmark\n\ngo 1.22\n' > "$project/go.mod"
{
  echo 'package benchmark'
  for ((i=0; i<functions; i++)); do
    printf 'func F%d(x int) bool { return x > %d }\n' "$i" "$i"
  done
} > "$project/p.go"
cat > "$project/p_test.go" <<'GO'
package benchmark
import "testing"
func TestBaseline(t *testing.T) { if !F0(1) { t.Fatal("unexpected failure") } }
GO
for mode in true false; do
  label="stream_${mode}"
  echo "Running $label: $functions boundary mutants, $workers workers" >&2
  (
    cd "$project"
    if [[ $(uname -s) == Linux ]]; then
      /usr/bin/time -f 'wall_seconds=%e\npeak_rss_kib=%M' -o "$output/$label.metrics" \
        "$cli" --no-cache --progress=false --operators boundary --workers "$workers" \
        --stream-results="$mode" --format json --output "$output/$label.json" .
    elif [[ $(uname -s) == Darwin ]]; then
      /usr/bin/time -l -o "$output/$label.metrics" \
        "$cli" --no-cache --progress=false --operators boundary --workers "$workers" \
        --stream-results="$mode" --format json --output "$output/$label.json" .
    else
      echo "only Linux and macOS process-RSS reporting are supported" >&2
      exit 2
    fi
  )
done
python3 - "$output/stream_true.json" "$output/stream_false.json" <<'PY'
import json, sys
stream, normal = [json.load(open(p)) for p in sys.argv[1:]]
identity = lambda r: [(item['mutation']['id'], item['verdict']) for item in r['results']]
assert stream['complete'] and normal['complete'], 'incomplete run'
assert stream['summary'] == normal['summary'], 'summary mismatch'
assert identity(stream) == identity(normal), 'mutation ordering/verdict mismatch'
print('PASS: streaming and in-memory reports have identical verdicts, order and summary')
PY
echo "Results and peak-RSS logs: $output"
