#!/usr/bin/env bash
# Triage for the operators and evidence added without a compiler in the
# authoring environment. Runs the cheap checks first, then each new
# feature's tests as its own group, and prints one PASS/FAIL line per
# group so a failure points at one feature rather than the whole suite.
#
#   ./scripts/verify-new-work.sh          # everything
#   VERBOSE=1 ./scripts/verify-new-work.sh   # show the output of failing groups
#
# Exit status is non-zero if any group failed. Paste the failing group's
# output (VERBOSE=1) to get it fixed.
set -u
cd "$(dirname "$0")/.."

fail=0
run() {
  local name=$1; shift
  local out
  if out=$("$@" 2>&1); then
    printf 'PASS  %s\n' "$name"
  else
    printf 'FAIL  %s\n' "$name"
    fail=1
    if [ "${VERBOSE:-0}" = 1 ]; then printf '%s\n\n' "$out" | sed 's/^/      /'; fi
  fi
}

gofmt_clean() {
  local bad
  bad=$(gofmt -l . 2>&1)
  [ -z "$bad" ] || { printf '%s\n' "$bad"; return 1; }
}

run "gofmt -l ."              gofmt_clean
run "go build ./..."          go build ./...
run "go vet ./..."            go vet ./...

# internal/frontend: one group per feature.
fe() { go test ./internal/frontend -run "$1" -count=1; }
run "frontend: dead-store equivalence"  fe 'TestDeadStore'
run "frontend: statement operator"      fe 'TestStatement'
run "frontend: condition operator"      fe 'TestCondition'
run "frontend: returnvalue operator"    fe 'TestReturnValue'
run "frontend: connective operator"     fe 'TestConnective'
run "frontend: bounds operator"         fe 'TestBounds'
run "frontend: discarded-result proof"  fe 'TestDiscardedResult'
run "frontend: everything else"         go test ./internal/frontend -count=1

# internal/runner: assertion scoping fix and crash evidence.
run "runner: assertions and crashes"    go test ./internal/runner -run 'TestExtract|TestParseTestingLogLine' -count=1
run "runner: everything else"           go test ./internal/runner -count=1

run "report, config, analysis, model"   go test ./internal/report ./internal/config ./internal/analysis ./internal/model ./internal/compare -count=1

# End to end: builds the binary and runs it on the fixtures.
it() { go test ./tests/integration -run "$1" -count=1; }
run "e2e: assertion attribution"  it 'TestAssertionAttributionEndToEnd'
run "e2e: statement deletion"     it 'TestStatementDeletion'
run "e2e: condition negation"     it 'TestConditionNegationEndToEnd'
run "e2e: return value"           it 'TestReturnValueEndToEnd'
run "e2e: connective"             it 'TestConnectiveEndToEnd'
run "e2e: bounds"                 it 'TestBoundsEndToEnd'
run "e2e: discarded result"       it 'TestDiscardedResultEndToEnd'
run "e2e: everything else"        go test ./tests/integration -count=1

run "go test ./... (whole module)" go test ./... -count=1
if [ -x examples/run-all.sh ]; then
  run "examples/run-all.sh" ./examples/run-all.sh
fi

echo
if [ "$fail" = 0 ]; then echo "All groups passed."; else echo "Some groups failed; rerun with VERBOSE=1 for details."; fi
exit "$fail"
