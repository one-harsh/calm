#!/usr/bin/env bash
# Copyright 2026 The CALM Authors
# SPDX-License-Identifier: Apache-2.0
#
# USER-RUN. Builds the local benchmark branch and the three fixture branches for
# the paired-run benchmark, entirely from the committed patch files beside
# this script. Local refs only: it never pushes and never touches remotes.
#
# It refuses to run unless the tracked working tree is clean and the recorded
# base commit is in the current history, and it aborts (rather than clobbering)
# if any target branch already exists — so a re-run is safe. Delete the branches
# to redo.
#
# Layout it creates:
#   bench/main   = base + the stripped contributor-directive commit  (the pin)
#   fixture/t1   = bench/main + one commit from t1.patch
#   fixture/t2   = bench/main + one commit from t2.patch
#   fixture/t5   = bench/main + one commit from t5.patch
#
# The runner cherry-picks a fixture commit onto the pin inside a disposable
# clone; each fixture branch's single commit is exactly that cherry-pick source.
set -euo pipefail

# The commit the fixtures were authored against (also the sweep pin's parent).
# The benchmark substrate is cut from main WITHOUT the harness commit: clones
# must never contain benchmarks/paired-run (checkers and fixture patches are
# the benchmark's answer key).
BASE_SHA="a15896e80719115a30335fd889bee915dbf11d01"

# Resolve locations before any branch switch (the patches and this script may
# live in commits that are not present in BASE_SHA's tree).
FIXTURES_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
cd "$(git -C "$FIXTURES_DIR" rev-parse --show-toplevel)"

# Neutral, truthful-sounding commit messages. A benchmark cell can read the git
# log, so nothing here may reveal that these commits seed the benchmark.
BENCH_MSG="Relocate adapter tooling guidance out of the contributor directive"
T1_MSG="Simplify the session snapshot query"
T2_MSG="Drop a redundant session service unit test file"
T5_MSG="Fold source-label scoping into the search predicate"

# --- preflight -------------------------------------------------------------
git update-index -q --refresh || true
if ! git diff-index --quiet HEAD --; then
  echo "refuse: tracked files have uncommitted changes; commit or stash first" >&2
  exit 1
fi
if ! git cat-file -e "${BASE_SHA}^{commit}" 2>/dev/null; then
  echo "refuse: recorded base commit $BASE_SHA not found (git fetch / pull --ff-only?)" >&2
  exit 1
fi
if ! git merge-base --is-ancestor "$BASE_SHA" HEAD; then
  echo "refuse: recorded base $BASE_SHA is not an ancestor of HEAD" >&2
  exit 1
fi
for b in bench/main fixture/t1 fixture/t2 fixture/t5; do
  if git show-ref --verify --quiet "refs/heads/$b"; then
    echo "refuse: branch $b already exists; delete it to re-run (git branch -D $b)" >&2
    exit 1
  fi
done
for p in claude-md-strip.patch t1.patch t2.patch t5.patch; do
  [ -f "$FIXTURES_DIR/$p" ] || { echo "refuse: missing patch $FIXTURES_DIR/$p" >&2; exit 1; }
done

START_BRANCH="$(git rev-parse --abbrev-ref HEAD)"

# Copy the patches out of the tree: checking out bench/main at BASE_SHA removes
# the (later-committed) fixtures/ dir from the working tree, so read the patches
# from a location that survives the branch switch.
TMP="$(mktemp -d)"
cleanup() { git checkout -q "$START_BRANCH" 2>/dev/null || true; rm -rf "$TMP"; }
trap cleanup EXIT
cp "$FIXTURES_DIR"/claude-md-strip.patch "$FIXTURES_DIR"/t1.patch \
   "$FIXTURES_DIR"/t2.patch "$FIXTURES_DIR"/t5.patch "$TMP/"

apply_commit() { # <patch-file> <message>
  git apply --index "$TMP/$1"
  git commit -s -q -m "$2"
}

# --- bench/main: base + stripped contributor directive ---------------------
git checkout -q -b bench/main "$BASE_SHA"
apply_commit claude-md-strip.patch "$BENCH_MSG"
BENCH_SHA="$(git rev-parse HEAD)"

# --- fixture branches: one commit each, off bench/main ---------------------
git checkout -q -b fixture/t1 bench/main
apply_commit t1.patch "$T1_MSG"
T1_SHA="$(git rev-parse HEAD)"

git checkout -q -b fixture/t2 bench/main
apply_commit t2.patch "$T2_MSG"
T2_SHA="$(git rev-parse HEAD)"

git checkout -q -b fixture/t5 bench/main
apply_commit t5.patch "$T5_MSG"
T5_SHA="$(git rev-parse HEAD)"

# cleanup trap returns us to START_BRANCH.

cat <<EOF

Benchmark branches created (local only — nothing was pushed).

  pinned_sha (bench/main): $BENCH_SHA
  fixture/t1:              $T1_SHA
  fixture/t2:              $T2_SHA
  fixture/t5:              $T5_SHA

Wire the shas in before the dry-run gate:

1) benchmarks/paired-run/suite.yaml — set each task's clone_setup.fixture:
     t1:  fixture: $T1_SHA
     t2:  fixture: $T2_SHA
     t5:  fixture: $T5_SHA
   (t3, t4, t6, and t-smoke stay 'fixture: none'.)

2) ~/.calm/paired-run.config.json — set:
     "pinned_sha": "$BENCH_SHA"

Undo everything: git branch -D bench/main fixture/t1 fixture/t2 fixture/t5
EOF
