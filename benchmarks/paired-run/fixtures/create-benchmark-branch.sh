#!/usr/bin/env bash
# Copyright 2026 The CALM Authors
# SPDX-License-Identifier: Apache-2.0
#
# USER-RUN. Builds the four local benchmark substrate branches for the paired-run
# benchmark from the committed patch files beside this script. Local refs only:
# it never pushes and never touches remotes.
#
# Each substrate is an ORPHAN branch: a single whole-tree import commit (the base
# tree + the stripped contributor directive, plus the task's fixture where it has
# one), followed by the SAME neutral cover commits. A seeded defect therefore
# never exists as any findable commit diff — it lives inside the whole-tree
# import, and the history shape is identical across every task.
#
#   bench2/base = base tree + strip              (t3/t4/t7/t8/t-smoke run here)
#   bench2/t1   = base tree + strip + t1 fixture
#   bench2/t2   = base tree + strip + t2 fixture
#   bench2/t5   = base tree + strip + t5 fixture
#
# Earlier sweeps' substrate branches (bench/*) are the recorded provenance of
# their results files and are never rebuilt or deleted; each sweep gets a fresh
# prefix.
#
# It refuses if any target branch already exists or the tracked tree is dirty,
# and it aborts if the base commit is missing. Safe to re-run after deleting the
# four branches.
set -euo pipefail

# The commit whose tree seeds every substrate (its code == the sweep pin's code).
# The in-repo benchmark harness is stripped from the import below, so cells
# carry only product code — never the suite, checkers, or oracle fixtures.
BASE_SHA="aba4585"

FIXTURES_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
cd "$(git -C "$FIXTURES_DIR" rev-parse --show-toplevel)"

# Neutral, boring, jargon-free commit messages. Benchmark cells read git log, so
# nothing here may hint at a benchmark or a seeded change. The cover messages
# and diffs are identical across all four branches.
IMPORT_MSG="Import project sources"
COVER_A_MSG="Describe CALM as a sidecar in the README opener"
COVER_B_MSG="Document the calm server run entrypoint"

PATCHES=(claude-md-strip.patch t1.patch t2.patch t5.patch cover-a.patch cover-b.patch)

# --- preflight -------------------------------------------------------------
git update-index -q --refresh || true
if ! git diff-index --quiet HEAD --; then
  echo "refuse: tracked files have uncommitted changes; commit or stash first" >&2
  exit 1
fi
if ! git cat-file -e "${BASE_SHA}^{commit}" 2>/dev/null; then
  echo "refuse: base commit $BASE_SHA not found (git fetch / pull --ff-only?)" >&2
  exit 1
fi
for b in bench2/base bench2/t1 bench2/t2 bench2/t5; do
  if git show-ref --verify --quiet "refs/heads/$b"; then
    echo "refuse: branch $b already exists; delete it to re-run (git branch -D $b)" >&2
    exit 1
  fi
done
for p in "${PATCHES[@]}"; do
  [ -f "$FIXTURES_DIR/$p" ] || { echo "refuse: missing patch $FIXTURES_DIR/$p" >&2; exit 1; }
done

# Copy patches out of the tree; the import worktree checks out a base that lacks
# the fixtures/ dir, so read patches from a location that survives.
TMP="$(mktemp -d)"
cleanup() { rm -rf "$TMP"; }
trap cleanup EXIT
for p in "${PATCHES[@]}"; do cp "$FIXTURES_DIR/$p" "$TMP/"; done

# build_substrate <branch> [fixture-patch]
# Materializes the seeded tree in a throwaway worktree at BASE_SHA, commits it as
# an orphan root, then layers the identical cover commits on top.
build_substrate() {
  local branch="$1" fixture="${2:-}"
  local wt
  wt="$(mktemp -d)"
  git worktree add --quiet --detach "$wt" "$BASE_SHA"
  (
    cd "$wt"
    # Cells carry only product code: the harness, checkers, and oracle fixtures
    # must never exist in a substrate tree.
    rm -rf benchmarks
    git apply "$TMP/claude-md-strip.patch"
    [ -n "$fixture" ] && git apply "$TMP/$fixture"
    git checkout --quiet --orphan "$branch"
    git add -A
    git commit -s -q -m "$IMPORT_MSG"
    git apply "$TMP/cover-a.patch"; git add -A; git commit -s -q -m "$COVER_A_MSG"
    git apply "$TMP/cover-b.patch"; git add -A; git commit -s -q -m "$COVER_B_MSG"
  )
  git worktree remove --force "$wt"
}

build_substrate bench2/base
build_substrate bench2/t1 t1.patch
build_substrate bench2/t2 t2.patch
build_substrate bench2/t5 t5.patch

BASE_TIP="$(git rev-parse bench2/base)"
T1_TIP="$(git rev-parse bench2/t1)"
T2_TIP="$(git rev-parse bench2/t2)"
T5_TIP="$(git rev-parse bench2/t5)"

cat <<EOF

Benchmark substrates created (local only — nothing was pushed).

Substrate tips:
    bench2/base: $BASE_TIP
    bench2/t1:   $T1_TIP
    bench2/t2:   $T2_TIP
    bench2/t5:   $T5_TIP

Wire the tips in before the dry-run gate:

1) benchmarks/paired-run/suite.yaml — set each task's clone_setup.fixture to the
   substrate tip that task checks out:
     t1:       fixture: $T1_TIP
     t2:       fixture: $T2_TIP
     t5:       fixture: $T5_TIP
     t3:       fixture: $BASE_TIP
     t4:       fixture: $BASE_TIP
     t7:       fixture: $BASE_TIP
     t8:       fixture: $BASE_TIP
     t-smoke:  fixture: $BASE_TIP

2) ~/.calm/paired-run.config.json — set:
     "pinned_sha": "$BASE_TIP"

Undo everything: git branch -D bench2/base bench2/t1 bench2/t2 bench2/t5
EOF
