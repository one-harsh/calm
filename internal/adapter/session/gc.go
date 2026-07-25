// Copyright 2026 The CALM Authors
// SPDX-License-Identifier: Apache-2.0

package session

import (
	"os"
	"path/filepath"
	"strings"
	"time"
)

// GC reaps conversation directories idle beyond twice the longest session TTL
// (floored at 24h) and sweeps orphaned atomic-write temp files from directories
// still in use. It is invoked explicitly — the CLI samples it from a fraction
// of invocations, so no daemon is needed (AD05). One entry failing to reap is
// skipped, not fatal.
func GC(root string, sessionTTL time.Duration) error {
	idle := 2 * sessionTTL
	if floor := 48 * time.Hour; idle < floor {
		idle = floor
	}
	cutoff := time.Now().Add(-idle)
	entries, err := os.ReadDir(filepath.Join(root, "sessions"))
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		dir := filepath.Join(root, "sessions", e.Name())
		if reapIfIdle(dir, cutoff) {
			continue
		}
		sweepOrphans(dir, cutoff)
	}
	return nil
}

func reapIfIdle(dir string, cutoff time.Time) bool {
	if !idleSince(dir, cutoff) {
		return false
	}
	// Advisory locks do not prevent deletion: the reap re-checks and removes
	// under the directory's own lock, so a resumed conversation is either
	// mid-invocation (lock busy — skip) or has saved fresh state (re-check
	// aborts). The unlock/remove ordering is platform-specific (lock_*.go).
	unlock, ok, err := (&store{dir: dir}).tryLock()
	if err != nil || !ok {
		return false
	}
	if fi, serr := os.Stat(filepath.Join(dir, stateFileName)); serr == nil && fi.ModTime().After(cutoff) {
		unlock()
		return false
	}
	return removeAllLocked(dir, unlock)
}

func idleSince(dir string, cutoff time.Time) bool {
	marker, err := os.Stat(filepath.Join(dir, stateFileName))
	if err != nil {
		marker, err = os.Stat(dir)
	}
	return err == nil && !marker.ModTime().After(cutoff)
}

func sweepOrphans(dir string, cutoff time.Time) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, e := range entries {
		if e.IsDir() || !strings.Contains(e.Name(), ".tmp.") {
			continue
		}
		if info, err := e.Info(); err == nil && info.ModTime().Before(cutoff) {
			_ = os.Remove(filepath.Join(dir, e.Name()))
		}
	}
}
