#!/usr/bin/env bash
set -euo pipefail

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT

cd "$root"
go build -o "$tmp/mutation-judge" ./cmd/mutation-judge

run() {
  printf '\n\n=== %s ===\n' "$1"
  shift
  "$tmp/mutation-judge" --no-cache "$@"
}

run "boundary survivor" --operators boundary ./examples/boundary
run "boundary fixed" --operators boundary ./examples/boundary_fixed
run "boundary equivalent suppression" --operators boundary ./examples/equivalent
run "boolean dead-store equivalence" --operators boolean ./examples/deadstore
run "statement deletion survivor" --operators statement ./examples/statement
run "condition negation" --operators condition ./examples/condition
run "return value survivor" --operators returnvalue ./examples/returnvalue
run "discarded result equivalence" --operators returnvalue ./examples/discarded
run "connective swap" --operators connective ./examples/connective
run "slice bounds" --operators bounds ./examples/bounds
run "boolean attribution" --operators boolean ./examples/boolean
run "full selected tests" --operators boolean ./examples/test_selection
run "filtered selected tests" --operators boolean --test-run '^TestVIPDiscount$' ./examples/test_selection
run "arithmetic and invalid" --operators arithmetic ./examples/arithmetic
run "generated excluded" --operators boolean ./examples/generated
run "generated included" --operators boolean --include-generated ./examples/generated
run "error-return survivor" --operators errorreturn ./examples/errorreturn
run "switch case deletion" --operators switch ./examples/switch
run "loop skip" --operators loop ./examples/loop
run "channel buffer mutation" --operators channel ./examples/channel
run "assignment survivor" --operators assignment ./examples/assignment
run "relational survivor" --operators relational ./examples/relational
run "literal survivor" --operators literal ./examples/literal
run "sarif output" --operators boundary --format sarif ./examples/boundary
run "github annotations (survivor)" --operators boundary --format github ./examples/boundary
run "github annotations (clean)" --operators boundary --format github ./examples/boundary_fixed

# Reuse the packaged tests in one scratch module so the production path
# and mutant ID stay stable when the test is repaired.
printf '\n\n=== compare and trend ===\n'
demo="$tmp/compare-demo"
mkdir -p "$demo"
cat > "$demo/go.mod" <<'EOF'
module comparedemo

go 1.22
EOF
cp "$root/examples/boundary/"{counter.go,counter_test.go} "$demo/"

analyze_demo() {
  (
    cd "$demo"
    "$tmp/mutation-judge" --no-cache --operators boundary --format json --output "$tmp/$1.json" .
  )
}

analyze_demo baseline
"$tmp/mutation-judge" record --label "PR #101" --history-file "$tmp/history.ndjson" "$tmp/baseline.json"

sed 's/^package boundaryfixed$/package boundary/' \
  "$root/examples/boundary_fixed/counter_test.go" > "$demo/counter_test.go"
analyze_demo current
"$tmp/mutation-judge" record --label "PR #102" --history-file "$tmp/history.ndjson" "$tmp/current.json"

"$tmp/mutation-judge" compare --baseline "$tmp/baseline.json" --current "$tmp/current.json"
printf '\n'
"$tmp/mutation-judge" trend --history-file "$tmp/history.ndjson"

# Compare the same baseline with a revision where the site is gone.
printf '\n\n=== compare: removed mutant ===\n'
rm "$demo/counter_test.go"
cat > "$demo/counter.go" <<'EOF'
package boundary

func AlwaysTrue() bool { return true }
EOF
analyze_demo removed
"$tmp/mutation-judge" compare --baseline "$tmp/baseline.json" --current "$tmp/removed.json"

printf '\n\nAll non-Git examples completed successfully.\n'
