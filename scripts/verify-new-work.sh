#!/usr/bin/env bash
# Run the local qualification checks from any working directory.
set -euo pipefail

cd "$(dirname "${BASH_SOURCE[0]}")/.."

# Keep absolute fixture paths consistent with paths reported by Go and Git.
TMPDIR="$(cd "${TMPDIR:-/tmp}" && pwd -P)"
export TMPDIR

printf '\n=== Formatting ===\n'
unformatted="$(gofmt -l .)"
if [[ -n "$unformatted" ]]; then
  printf 'Unformatted Go files:\n%s\n' "$unformatted" >&2
  exit 1
fi

printf '\n=== Compile ===\n'
go build ./...

printf '\n=== Static analysis ===\n'
go vet ./...

printf '\n=== Tests ===\n'
go test -p 1 -count=1 ./...

printf '\n=== Packaged examples ===\n'
./examples/run-all.sh

printf '\nLocal qualification passed.\n'
