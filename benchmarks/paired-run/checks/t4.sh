#!/usr/bin/env bash
# Copyright 2026 The CALM Authors
# SPDX-License-Identifier: Apache-2.0
#
# Acceptance checker for t4 (add `--json` to `calm-capture search`).
#
# The agent never sees the oracle. After the agent's run, this copies a
# pre-written oracle test into the clone's capturecli package, then requires
# BOTH the oracle (schema + byte-identical default + degraded-carries-reason)
# and the full `task ci` gate to pass. On the unfixed tree the `--json` flag is
# absent, so the oracle fails and this exits nonzero.
set -euo pipefail

CLONE="$1"
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ASSETS="$SCRIPT_DIR/testdata"

PKG_DIR="$CLONE/internal/adapter/capturecli"
if [ ! -d "$PKG_DIR" ]; then
  echo "t4: capturecli package not found at $PKG_DIR" >&2
  exit 1
fi

# Inject the oracle into the package under test.
cp "$ASSETS/t4_acceptance_test.go" "$PKG_DIR/t4_acceptance_test.go"

# The default-mode golden is read by the oracle via this absolute path. It is
# kept OUT of the module tree so it never trips lint / license / format gates,
# and exported so the copies of `go test` that `task ci` spawns inherit it.
export T4_GOLDEN="$ASSETS/t4_default_golden.txt"
if [ ! -f "$T4_GOLDEN" ]; then
  echo "t4: default-mode golden missing at $T4_GOLDEN" >&2
  exit 1
fi

cd "$CLONE"

# 1) Oracle: fast, focused signal on the feature contract.
go test -tags=mocks -count=1 -run '^TestT4Search' ./internal/adapter/capturecli/

# 2) Full CI gate must be green with the feature in place.
task ci

echo "t4 acceptance: oracle green + task ci green"
