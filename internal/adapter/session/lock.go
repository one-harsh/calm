// Copyright 2026 The CALM Authors
// SPDX-License-Identifier: Apache-2.0

package session

import (
	"os"
	"path/filepath"
)

// acquire opens the conversation's sibling lock file and takes the exclusive
// advisory lock. The lock file is never deleted (see lockPath), so the classic
// unlink pathology — a lock held on an orphaned inode excluding nobody — cannot
// occur and acquisition needs no inode revalidation.
func acquire(path string, block bool) (unlock func(), acquired bool, err error) {
	//nolint:gosec // path is the adapter's own state-lock file under $CALM_HOME, not attacker-controlled
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, false, err
	}
	ok, lerr := lockFile(f, block)
	if lerr != nil {
		_ = f.Close()
		return nil, false, lerr
	}
	if !ok {
		_ = f.Close()
		return nil, false, nil
	}
	return func() { unlockFile(f) }, true, nil
}

func (s *store) lock() (func(), error) {
	// The sessions parent is created outside the lock — it is never reaped.
	// The conversation directory itself is created only while HOLDING the
	// sibling lock, so creation can never interleave with a reap's RemoveAll:
	// a waiting invocation recreates the directory strictly after the reap.
	if err := os.MkdirAll(filepath.Dir(s.dir), 0o700); err != nil {
		return nil, err
	}
	unlock, _, err := acquire(s.lockPath(), true)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(s.dir, 0o700); err != nil {
		unlock()
		return nil, err
	}
	// MkdirAll leaves a pre-existing directory's broader mode untouched, so
	// owner-only is re-asserted on every acquisition. POSIX bits are advisory
	// on Windows: the default %USERPROFILE% placement inherits an owner-scoped
	// ACL, and a CALM_HOME override outside the profile leaves the ACL to the
	// operator.
	_ = os.Chmod(s.dir, 0o700) //nolint:gosec // 0700 is owner-only for a directory; the execute bit is traversal, not a wider grant

	// Network filesystems are unsupported: advisory locks over NFS are unreliable.
	return unlock, nil
}

// tryLock acquires without blocking; ok=false means another process holds the
// lock. It never creates the session directory — GC must not resurrect one.
func (s *store) tryLock() (func(), bool, error) {
	return acquire(s.lockPath(), false)
}
