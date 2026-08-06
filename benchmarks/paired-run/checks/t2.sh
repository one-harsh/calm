#!/usr/bin/env bash
# Copyright 2026 The CALM Authors
# SPDX-License-Identifier: Apache-2.0
#
# Acceptance for t2: a deleted branch-heavy test file sank the coverage gate.
# The agent restores it by writing meaningful tests for the uncovered branches.
# Accepts only when the full CI gate is green (coverage back above threshold),
# the affected package's worst-covered functions clear per-function floors, and
# a seeded behavior mutation is caught by the new tests (the no-assert detector).
set -euo pipefail

CLONE="${1:?usage: t2.sh <clone_dir>}"
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ASSETS="$SCRIPT_DIR/testdata"
PKG="./internal/session/..."
PROD_FILE="internal/session/service.go"

cd "$CLONE"
export CALM_TEST_PG_DSN="${CALM_TEST_PG_DSN:-postgres://postgres:postgres@localhost:5432/postgres?sslmode=disable}"

# 1) Full CI gate green — includes the coverage gate the deletion tripped.
task ci

# 2) Per-function floors for the affected package's worst-covered functions.
#    Floors sit a few points below the original-test coverage so a genuine
#    restoration clears them; they force real branch coverage of the error and
#    boundary paths rather than mere line execution.
cov="$(mktemp -t t2cov.XXXXXX)"
go test -tags=mocks -count=1 -covermode=atomic -coverprofile="$cov" $PKG >/dev/null
funccov="$(go tool cover -func="$cov")"
rm -f "$cov"

check_floor() { # <func> <floor%>
  local fn="$1" floor="$2" pct
  pct="$(printf '%s\n' "$funccov" | awk -v fn="$fn" '$1 ~ /service\.go:/ && $2==fn {gsub(/%/,"",$3); print $3}')"
  [ -n "$pct" ] || { echo "t2: function $fn not found in session coverage" >&2; exit 1; }
  awk -v p="$pct" -v f="$floor" -v fn="$fn" \
    'BEGIN{ if (p+0 < f+0){ printf "t2: %s coverage %s%% below floor %s%%\n", fn, p, f > "/dev/stderr"; exit 1 } }'
}
check_floor Create 82
check_floor DeleteByID 75
check_floor deleteLockedSession 70
check_floor DeleteAll 80
check_floor Delete 90

# 3) Mutation probe: reintroduce one behavior mutation into the production code
#    and require the restored tests to catch it. Assertion-free tests that only
#    execute lines will not. Revert afterward (the clone is disposable, but keep
#    it hermetic).
git apply "$ASSETS/t2_mutation.patch"
if go test -tags=mocks -count=1 $PKG >/dev/null 2>&1; then
  git checkout -- "$PROD_FILE"
  echo "t2: seeded mutation NOT caught by the restored tests (coverage-gaming)" >&2
  exit 1
fi
git checkout -- "$PROD_FILE"

echo "t2: PASS"
