#!/usr/bin/env bash
# Copyright 2026 The CALM Authors
# SPDX-License-Identifier: Apache-2.0
#
# Acceptance checker for t3 (add `replace_all` to the adapter's calm_edit_file).
#
# The agent never sees the oracle. After the agent's run, this copies a
# pre-written oracle test into the clone's mcp package, then requires BOTH the
# oracle (schema/description expose the flag; single/multi/zero-match behavior;
# capture/label on multi-edit) and the full `task ci` gate to pass. On the
# unfixed tree the flag is absent from the schema and silently ignored at
# runtime, so the oracle fails and this exits nonzero.
set -euo pipefail

CLONE="$1"
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ASSETS="$SCRIPT_DIR/testdata"

PKG_DIR="$CLONE/internal/adapter/mcp"
if [ ! -d "$PKG_DIR" ]; then
  echo "t3: mcp package not found at $PKG_DIR" >&2
  exit 1
fi

# Inject the oracle into the package under test. It is package mcp_test, so it
# reuses the package's own in-process JSON-RPC harness helpers.
cp "$ASSETS/t3_acceptance_test.go" "$PKG_DIR/t3_acceptance_test.go"

cd "$CLONE"

# 1) Oracle: fast, focused signal on the feature contract.
go test -tags=mocks -count=1 -run '^TestReplaceAllOracle' ./internal/adapter/mcp/

# 2) Full CI gate must be green with the feature in place.
task ci

echo "t3 acceptance: oracle green + task ci green"
