#!/usr/bin/env bash
# Compare two Mutation Judge executables on identical many-file Go inputs.
# Usage: bash scripts/benchmark-r07-s13.sh BASELINE_CLI UPDATED_CLI [files=80] [mutants=12] [workers=4] [output-dir]
set -euo pipefail
if (( $# < 2 )); then
  echo "usage: $0 BASELINE_CLI UPDATED_CLI [files] [mutants] [workers] [output-dir]" >&2
  exit 2
fi
baseline=$1
updated=$2
file_count=${3:-80}
mutant_count=${4:-12}
workers=${5:-4}
for n in "$file_count" "$mutant_count" "$workers"; do
  if ! [[ "$n" =~ ^[1-9][0-9]*$ ]]; then
    echo 'files, mutants and workers must be positive integers' >&2
    exit 2
  fi
done
for bin in "$baseline" "$updated"; do
  if ! [[ -x "$bin" ]]; then echo "not executable: $bin" >&2; exit 2; fi
done
baseline=$(cd "$(dirname "$baseline")" && pwd)/$(basename "$baseline")
updated=$(cd "$(dirname "$updated")" && pwd)/$(basename "$updated")
if (( $# >= 6 )); then
  mkdir -p "$6"
  output=$(cd "$6" && pwd)
else
  output=$(mktemp -d "${TMPDIR:-/tmp}/mutation-judge-r07-s13.XXXXXXXX")
fi
project="$output/project"
mkdir -p "$project"
printf 'module example.test/mutation-judge-r07s13-benchmark\n\ngo 1.22\n' > "$project/go.mod"
for ((i=0; i<file_count; i++)); do
  printf -v file '%s/file_%05d.go' "$project" "$i"
  printf 'package p\nfunc F%d(x int) bool { return x >= %d }\n' "$i" "$i" > "$file"
done
cat > "$project/p_test.go" <<'GO'
package p
import "testing"
func TestSmoke(t *testing.T) { if !F0(1) { t.Fatal("smoke") } }
GO
# Warm Go's build cache, but always disable the Mutation Judge verdict cache.
(cd "$project" && go test ./... >/dev/null)
for mode in baseline updated; do
  if [[ "$mode" == baseline ]]; then cli=$baseline; else cli=$updated; fi
  (
    cd "$project"
    if [[ $(uname -s) == Linux ]]; then
      /usr/bin/time -f 'wall_seconds=%e\npeak_rss_kib=%M' -o "$output/$mode.metrics" \
        "$cli" --no-cache --progress=false --operators boundary,relational \
        --max-mutants "$mutant_count" --workers "$workers" --stream-results \
        --format json --output "$output/$mode.json" ./...
    elif [[ $(uname -s) == Darwin ]]; then
      /usr/bin/time -l -o "$output/$mode.metrics" \
        "$cli" --no-cache --progress=false --operators boundary,relational \
        --max-mutants "$mutant_count" --workers "$workers" --stream-results \
        --format json --output "$output/$mode.json" ./...
    else
      echo 'RSS benchmark supports Linux and macOS' >&2
      exit 2
    fi
  )
done
python3 - "$output/baseline.json" "$output/updated.json" <<'PY'
import json, sys
baseline, updated = [json.load(open(x)) for x in sys.argv[1:]]
assert baseline['complete'] and updated['complete'], 'an analysis was incomplete'
assert baseline['summary'] == updated['summary'], 'summaries differ'
assert baseline['bounds']['discovered_before_bound'] == updated['bounds']['discovered_before_bound'], 'discovery counts differ'
def rows(r):
    return [(x['mutation'], x['verdict']) for x in r['results']]
assert rows(baseline) == rows(updated), 'IDs, patches or verdicts differ'
print('PASS: identical discovery count, mutation data, verdicts and scores')
PY
echo "Measurements and complete reports: $output"
