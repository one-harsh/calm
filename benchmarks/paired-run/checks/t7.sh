#!/usr/bin/env bash
# Copyright 2026 The CALM Authors
# SPDX-License-Identifier: Apache-2.0
#
# Acceptance for t7: implement the four /v1/manage/* operations against the
# committed OpenAPI spec, namespace-scoped, with the repository's isolation
# discipline. The task is additive, so there is no confinement check — but the
# prompt requires integration scenarios, so the agent's own coverage is
# verified before a pre-written oracle (which the benchmarked agent never sees)
# is injected and run against real Postgres, followed by the full CI gate. On
# the unfixed tree every manage endpoint answers 501, so the oracle's first
# spec-conformant-200 assertion fails and this exits nonzero.
set -euo pipefail

CLONE="${1:?usage: t7.sh <clone_dir>}"
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ASSETS="$SCRIPT_DIR/testdata"
ORACLE="$ASSETS/t7_acceptance_test.go"

if [ ! -f "$ORACLE" ]; then
	echo "t7: oracle missing at $ORACLE" >&2
	exit 2
fi
if [ ! -d "$CLONE/test/integration" ]; then
	echo "t7: '$CLONE/test/integration' missing — not a CALM clone?" >&2
	exit 2
fi

cd "$CLONE"
export CALM_TEST_PG_DSN="${CALM_TEST_PG_DSN:-postgres://postgres:postgres@localhost:5432/postgres?sslmode=disable}"

# 1) The prompt requires integration scenarios for the manage surface. Checked
#    before the oracle is injected so the oracle never counts as the agent's
#    own work. The prompts never require committing, so the agent's additions
#    live in the working tree while HEAD stays the seeded fixture commit;
#    `status --porcelain` also surfaces new (untracked) files.
changed="$(git status --porcelain | sed 's/^...//')"
added_integration_test=0
for f in $changed; do
	case "$f" in
	test/integration/*_test.go) added_integration_test=1 ;;
	esac
done
if [ "$added_integration_test" -ne 1 ]; then
	echo "t7: no integration scenario added under test/integration/" >&2
	exit 1
fi

# 2) Independent oracle: listing, client filter, cross-namespace invisibility,
#    delete + post-delete content invisibility, and the client surface, all
#    driven through the generated client so spec conformance is structural.
cp "$ORACLE" test/integration/t7_acceptance_test.go
go test -tags=mocks -count=1 -run '^TestT7Oracle' ./test/integration/

# 3) Full CI gate must be green with the endpoints in place (`task test` runs
#    the integration suite the prompt names, the agent's scenarios included).
task ci

echo "t7: PASS"
