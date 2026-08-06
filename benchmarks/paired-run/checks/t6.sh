#!/usr/bin/env bash
# Copyright 2026 The CALM Authors
# SPDX-License-Identifier: Apache-2.0
#
# t6 acceptance: bound stored chunk size at ingest (so a large single-element
# capture becomes multiple byte-exact pageable chunks) and make budget_bytes
# govern document-order page fill. Injects a pre-written oracle integration test
# (the benchmarked agent never sees it), runs it against real Postgres — three
# cases that on the unfixed tree trip on each defect independently plus the
# end-to-end paging scenario — then asserts the full CI gate is green. Exit 0
# only if both pass.
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ASSETS="$SCRIPT_DIR/testdata"

CLONE="${1:?usage: t6.sh <clone_dir>}"
if [ ! -d "$CLONE/test/integration" ]; then
	echo "t6: '$CLONE/test/integration' missing — not a CALM clone?" >&2
	exit 2
fi

cp "$ASSETS/t6_acceptance_test.go" "$CLONE/test/integration/t6_acceptance_test.go"

export CALM_TEST_PG_DSN="${CALM_TEST_PG_DSN:-postgres://postgres:postgres@localhost:5432/postgres?sslmode=disable}"

cd "$CLONE"

# Oracle: focused signal on both fixes and the end-to-end paging scenario.
go test -tags=mocks -count=1 -run '^TestT6Oracle' ./test/integration/

# Full CI gate must be green with the fixes in place.
task ci

echo "t6: PASS"
