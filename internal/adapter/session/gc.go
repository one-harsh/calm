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
	// The lock is a sibling of dir, so RemoveAll never touches it and the lock
	// stays held through the removal on every platform. A partial RemoveAll
	// leaves the directory idle and still lock-guarded for the next cycle. The
	// sibling lock file itself is never reaped — deleting it would reintroduce
	// the unlink pathology; it is zero-length and bounded by conversation count.
	reaped := os.RemoveAll(dir) == nil
	unlock()
	return reaped
}

func idleSince(dir string, cutoff time.Time) bool {
	marker, err := os.Stat(filepath.Join(dir, stateFileName))
	if err != nil {
		marker, err = os.Stat(dir)
	}
	return err == nil && !marker.ModTime().After(cutoff)
}

// sweepOrphans reaps leftovers in a directory GC did not whole-reap: atomic-write
// temp files and aged event spools past the idle cutoff, and inflight event
// claims past the shorter stale-inflight age. Inflight claims are deleted unread
// — the same delete-not-replay guarantee Drain applies (AD06). The sweep runs
// under the conversation's lock: an unlocked sweep could observe an aged spool,
// race a live Record's spool append, and unlink freshly enqueued events. A busy
// conversation is skipped — its own invocations keep house.
func sweepOrphans(dir string, cutoff time.Time) {
	unlock, ok, err := (&store{dir: dir}).tryLock()
	if err != nil || !ok {
		return
	}
	defer unlock()
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	staleInflight := time.Now().Add(-staleInflightAge)
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		switch {
		case strings.Contains(name, ".tmp."), name == spoolFileName:
			removeIfModifiedBefore(dir, e, cutoff)
		case strings.HasPrefix(name, inflightPrefix):
			removeIfModifiedBefore(dir, e, staleInflight)
		}
	}
}

func removeIfModifiedBefore(dir string, e os.DirEntry, cutoff time.Time) {
	if info, err := e.Info(); err == nil && info.ModTime().Before(cutoff) {
		_ = os.Remove(filepath.Join(dir, e.Name()))
	}
}
