#!/usr/bin/env bash
# Copyright 2026 The CALM Authors
# SPDX-License-Identifier: Apache-2.0
#
# Acceptance for t8: section captured tool output on the structure a reader
# navigates by — a git diff per file per hunk, code on declaration boundaries —
# without disturbing capture identities. The agent's own unit tests are
# verified first, then a pre-written oracle (which the benchmarked agent never
# sees) runs each fixture through the pipeline as production composes it: the
# adapter's deriver chooses the capture's identity and content hints, and the
# sectioning stage turns the bytes into titled sections. On the unfixed tree
# the diff sections once for the whole capture and the code fixture sections on
# blank lines, so the oracle fails and this exits nonzero.
set -euo pipefail

CLONE="${1:?usage: t8.sh <clone_dir>}"
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ASSETS="$SCRIPT_DIR/testdata"
ORACLE="$ASSETS/t8_acceptance_test.go"
SECTIONING_PKG="internal/ingest/chunk"
DERIVER_PKG="internal/adapter/extract"

if [ ! -f "$ORACLE" ]; then
	echo "t8: oracle missing at $ORACLE" >&2
	exit 2
fi
for fixture in t8_multifile.diff t8_sample_go.txt; do
	if [ ! -f "$ASSETS/$fixture" ]; then
		echo "t8: fixture missing at $ASSETS/$fixture" >&2
		exit 2
	fi
done
if [ ! -d "$CLONE/$SECTIONING_PKG" ] || [ ! -d "$CLONE/$DERIVER_PKG" ]; then
	echo "t8: '$SECTIONING_PKG' or '$DERIVER_PKG' missing under $CLONE — not a CALM clone?" >&2
	exit 2
fi

# The oracle reads its fixtures through this absolute path. They are kept OUT
# of the module tree so they never trip lint / license / format gates, and
# exported so the copies of `go test` that `task ci` spawns inherit them.
export T8_FIXTURES="$ASSETS"

cd "$CLONE"

# 1) The prompt requires unit tests over representative fixtures. Checked
#    before the oracle is injected so the oracle never counts as the agent's
#    own work.
changed="$(git status --porcelain | sed 's/^...//')"
added_test=0
for f in $changed; do
	case "$f" in
	*_test.go) added_test=1 ;;
	esac
done
if [ "$added_test" -ne 1 ]; then
	echo "t8: no unit test (*_test.go) added for the new sectioning" >&2
	exit 1
fi

# 2) Independent oracle: per-hunk diff sections titled by file + hunk header,
#    code sections on declaration boundaries titled by what they contain, and
#    capture identities unchanged.
cp "$ORACLE" "$SECTIONING_PKG/t8_acceptance_test.go"
go test -tags=mocks -count=1 -run '^TestT8Oracle' "./$SECTIONING_PKG/"

# 3) Full CI gate must be green with the new sectioning in place.
task ci

echo "t8: PASS"
