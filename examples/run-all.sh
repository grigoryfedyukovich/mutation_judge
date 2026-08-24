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
run "sarif output" --operators boundary --format sarif ./examples/boundary
run "github annotations (survivor)" --operators boundary --format github ./examples/boundary
run "github annotations (clean)" --operators boundary --format github ./examples/boundary_fixed

# compare/record/trend are subcommands, not flags on the default
# command, so they bypass the run() helper above entirely (and don't
# take --no-cache -- there's no analysis to cache, just JSON files
# being read, diffed, and appended). This demo copies examples/boundary
# into a scratch module, analyzes it once with its checked-in weak
# test (a genuine SURVIVED), then patches just the test file in place
# and analyzes again -- the production file, and so the mutant's ID,
# never changes, so the comparison below shows a real KILLED fix
# rather than a misleading REMOVED/new pair, which is what comparing
# two already-different packages like boundary and boundary_fixed
# would show instead (their mutants never share an ID at all, since
# the file path itself is hashed into it).
printf '\n\n=== compare and trend ===\n'
demo="$tmp/compare-demo"
mkdir -p "$demo"
cat > "$demo/go.mod" <<'EOF'
module comparedemo

go 1.22
EOF
cp "$root/examples/boundary/counter.go" "$demo/counter.go"
sed -i.bak 's/^package boundary$/package comparedemo/' "$demo/counter.go" && rm "$demo/counter.go.bak"
cat > "$demo/counter_test.go" <<'EOF'
package comparedemo

import "testing"

// Deliberately omits n == 0 so the > to >= mutant survives.
func TestCountPositive(t *testing.T) {
	calls := 0
	CountPositive(2, func(int) { calls++ })
	CountPositive(-1, func(int) { calls++ })
	if calls != 1 {
		t.Fatalf("calls = %d, want 1", calls)
	}
}
EOF
(cd "$demo" && "$tmp/mutation-judge" --no-cache --operators boundary --format json --output baseline.json .)
"$tmp/mutation-judge" record --label "PR #101" --history-file "$demo/history.ndjson" "$demo/baseline.json"

cat > "$demo/counter_test.go" <<'EOF'
package comparedemo

import "testing"

func TestCountPositive(t *testing.T) {
	calls := 0
	CountPositive(2, func(int) { calls++ })
	CountPositive(-1, func(int) { calls++ })
	CountPositive(0, func(int) { calls++ })
	if calls != 1 {
		t.Fatalf("calls = %d, want 1", calls)
	}
}
EOF
(cd "$demo" && "$tmp/mutation-judge" --no-cache --operators boundary --format json --output current.json .)
"$tmp/mutation-judge" record --label "PR #102" --history-file "$demo/history.ndjson" "$demo/current.json"

"$tmp/mutation-judge" compare --baseline "$demo/baseline.json" --current "$demo/current.json"
printf '\n'
"$tmp/mutation-judge" trend --history-file "$demo/history.ndjson"

printf '\n\nAll non-Git examples completed successfully.\n'
