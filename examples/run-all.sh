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

printf '\n\nAll non-Git examples completed successfully.\n'
