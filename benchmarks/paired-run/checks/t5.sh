#!/usr/bin/env bash
# Copyright 2026 The CALM Authors
# SPDX-License-Identifier: Apache-2.0
#
# Acceptance for t5: a source-scoped session-isolation leak in one internal/db
# search leaf that the existing suite never covered (the gap is the point). The
# agent fixes the guard, adds a regression test, and documents which existing
# test should have caught it. Accepts only when an independent leak-probe oracle
# passes, the change is confined to the seeded DAL file plus tests, a regression
# test was added, DIAGNOSIS.md names the guard/isolation promise, and the full
# integration suite (agent's regression test included) is green.
set -euo pipefail

CLONE="${1:?usage: t5.sh <clone_dir>}"
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ASSETS="$SCRIPT_DIR/testdata"
SEEDED_FILE="internal/db/pg_textsearch.go"

cd "$CLONE"
export CALM_TEST_PG_DSN="${CALM_TEST_PG_DSN:-postgres://postgres:postgres@localhost:5432/postgres?sslmode=disable}"

# 1) Confinement (before injecting the oracle so it is not counted): the change
#    touches only the seeded DAL file, test files, and DIAGNOSIS.md — and at
#    least one test file was added (the required regression test).
changed="$(git status --porcelain | sed 's/^...//')"
added_test=0
for f in $changed; do
  case "$f" in
    "$SEEDED_FILE"|DIAGNOSIS.md) ;;
    *_test.go) added_test=1 ;;
    *) echo "t5: change outside allowed set (DAL file + tests + DIAGNOSIS.md): $f" >&2; exit 1 ;;
  esac
done
[ "$added_test" -eq 1 ] || { echo "t5: no regression test (*_test.go) added" >&2; exit 1; }
printf '%s\n' $changed | grep -qxF "$SEEDED_FILE" || {
  echo "t5: seeded file $SEEDED_FILE unchanged — no fix applied" >&2; exit 1; }

# 2) DIAGNOSIS.md names the guard construct and the violated isolation promise.
[ -f DIAGNOSIS.md ] || { echo "t5: DIAGNOSIS.md missing" >&2; exit 1; }
grep -Eiq 'exists|guard'      DIAGNOSIS.md || { echo "t5: DIAGNOSIS.md does not name the guard construct" >&2; exit 1; }
grep -Eiq 'isolation'         DIAGNOSIS.md || { echo "t5: DIAGNOSIS.md does not name the violated promise" >&2; exit 1; }
grep -Eiq 'namespace|session' DIAGNOSIS.md || { echo "t5: DIAGNOSIS.md does not scope the promise (namespace/session)" >&2; exit 1; }

# 3) Independent oracle: two sessions, one namespace; a source-scoped search in
#    one must not surface the other's content. Fails on the unfixed leak.
cp "$ASSETS/t5_acceptance_test.go" test/integration/t5_acceptance_test.go
go test -tags=mocks -count=1 -run '^TestT5Oracle' ./test/integration/

# 4) Full integration suite green (the agent's regression test runs here too).
go test -tags=mocks -count=1 ./test/integration/...

echo "t5: PASS"
