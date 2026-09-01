#!/usr/bin/env bash
# Runs the test suite and enforces a minimum coverage percentage over the
# internal packages. cmd/proem is excluded: it is a thin main() whose wiring
# lives in internal/app and is covered there.
set -euo pipefail

MIN="${COVERAGE_MIN:-95}"
PROFILE="${COVERAGE_PROFILE:-coverage.out}"

PKGS=$(go list ./internal/...)

go test $PKGS -race -count=1 -covermode=atomic -coverprofile="$PROFILE"

go tool cover -func="$PROFILE"

TOTAL=$(go tool cover -func="$PROFILE" | awk '/^total:/ {gsub(/%/,"",$3); print $3}')
echo
echo "internal coverage: ${TOTAL}% (minimum ${MIN}%)"

if awk -v t="$TOTAL" -v m="$MIN" 'BEGIN { exit !(t < m) }'; then
  echo "FAIL: coverage ${TOTAL}% is below the ${MIN}% minimum" >&2
  exit 1
fi
echo "OK"
