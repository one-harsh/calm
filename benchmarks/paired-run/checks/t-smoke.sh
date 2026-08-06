#!/usr/bin/env bash
# Copyright 2026 The CALM Authors
# SPDX-License-Identifier: Apache-2.0
#
# Smoke cell (quadrant Q4). Proves the harness pipe end-to-end on a trivial
# task: the agent writes SMOKE.md with one specific sentence and builds the
# capture CLI. Not a measurement — a liveness check that spawn, run, acceptance,
# and scoring all connect.
set -euo pipefail

CLONE="${1:?usage: t-smoke.sh <clone_dir>}"
SENTENCE="CALM keeps tool output out of the context window."

cd "$CLONE"

[ -f SMOKE.md ] || { echo "t-smoke: SMOKE.md missing" >&2; exit 1; }
grep -qF "$SENTENCE" SMOKE.md || { echo "t-smoke: SMOKE.md does not contain the required sentence" >&2; exit 1; }

# The build target must still succeed on the (essentially untouched) tree.
task build:capture

echo "t-smoke: PASS"
