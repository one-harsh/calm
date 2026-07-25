// Copyright 2026 The CALM Authors
// SPDX-License-Identifier: Apache-2.0

package session

import (
	"errors"
	"os"
)

// acquire opens the dedicated lock file and takes the exclusive advisory lock,
// re-acquiring on a fresh inode when a concurrent GC reap unlinked the file
// mid-acquisition — a lock held on an orphaned inode excludes nobody. Bounded
// retries: persistent staleness means the directory is being reaped and
// recreated under us, which the caller's load then surfaces honestly.
func acquire(path string, block bool) (unlock func(), acquired bool, err error) {
	for attempt := 0; attempt < 4; attempt++ {
		//nolint:gosec // path is the adapter's own state-lock file under $CALM_HOME, not attacker-controlled
		f, oerr := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
		if oerr != nil {
			return nil, false, oerr
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
		if lockStale(f, path) {
			unlockFile(f)
			continue
		}
		return func() { unlockFile(f) }, true, nil
	}
	return nil, false, errors.New("lock file repeatedly replaced during acquisition")
}

func (s *store) lock() (func(), error) {
	if err := os.MkdirAll(s.dir, 0o700); err != nil {
		return nil, err
	}
	// MkdirAll leaves a pre-existing directory's broader mode untouched, so
	// owner-only is re-asserted on every acquisition. POSIX bits are advisory
	// on Windows: the default %USERPROFILE% placement inherits an owner-scoped
	// ACL, and a CALM_HOME override outside the profile leaves the ACL to the
	// operator.
	_ = os.Chmod(s.dir, 0o700) //nolint:gosec // 0700 is owner-only for a directory; the execute bit is traversal, not a wider grant

	// Network filesystems are unsupported: advisory locks over NFS are unreliable.
	unlock, _, err := acquire(s.lockPath(), true)
	return unlock, err
}

// tryLock acquires without blocking; ok=false means another process holds the
// lock. It never creates the session directory — GC must not resurrect one.
func (s *store) tryLock() (func(), bool, error) {
	return acquire(s.lockPath(), false)
}
