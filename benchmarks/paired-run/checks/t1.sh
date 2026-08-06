#!/usr/bin/env bash
# Copyright 2026 The CALM Authors
# SPDX-License-Identifier: Apache-2.0
#
# Acceptance for t1: a scoping-guard defect in one internal/db read path made
# several integration scenarios fail together. The agent root-causes it and
# restores the guard with a minimal, single-file change, documenting the defect
# in DIAGNOSIS.md. Accepts only when the integration suite is green again, the
# working-tree change is confined to the one seeded production file plus
# DIAGNOSIS.md, and DIAGNOSIS.md names the guard construct and the isolation
# promise it violated.
set -euo pipefail

CLONE="${1:?usage: t1.sh <clone_dir>}"
SEEDED_FILE="internal/db/pg_events.go"

cd "$CLONE"
export CALM_TEST_PG_DSN="${CALM_TEST_PG_DSN:-postgres://postgres:postgres@localhost:5432/postgres?sslmode=disable}"

# 1) The suite the defect broke must be green again.
go test -tags=mocks -count=1 ./test/integration/...

# 2) Change confined to the one seeded file (+ DIAGNOSIS.md). The prompts never
#    require committing, so the agent's edits live in the working tree while HEAD
#    stays the seeded fixture commit; `status --porcelain` also surfaces the new
#    (untracked) DIAGNOSIS.md, which a plain `git diff` would miss. Ignored build
#    and coverage artifacts do not appear here (.gitignore covers them).
changed="$(git status --porcelain | sed 's/^...//')"
for f in $changed; do
  case "$f" in
    "$SEEDED_FILE"|DIAGNOSIS.md) ;;
    *) echo "t1: change outside allowed set (seeded file + DIAGNOSIS.md): $f" >&2; exit 1 ;;
  esac
done
printf '%s\n' $changed | grep -qxF "$SEEDED_FILE" || {
  echo "t1: seeded file $SEEDED_FILE unchanged — no fix applied" >&2; exit 1; }

# 3) DIAGNOSIS.md names the guard construct and the violated isolation promise.
[ -f DIAGNOSIS.md ] || { echo "t1: DIAGNOSIS.md missing" >&2; exit 1; }
grep -Eiq 'exists|guard'      DIAGNOSIS.md || { echo "t1: DIAGNOSIS.md does not name the guard construct" >&2; exit 1; }
grep -Eiq 'isolation'         DIAGNOSIS.md || { echo "t1: DIAGNOSIS.md does not name the violated promise" >&2; exit 1; }
grep -Eiq 'namespace|session' DIAGNOSIS.md || { echo "t1: DIAGNOSIS.md does not scope the promise (namespace/session)" >&2; exit 1; }

echo "t1: PASS"
