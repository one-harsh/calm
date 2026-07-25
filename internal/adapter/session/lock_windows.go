// Copyright 2026 The CALM Authors
// SPDX-License-Identifier: Apache-2.0

//go:build windows

package session

import (
	"errors"
	"os"

	"golang.org/x/sys/windows"
)

func lockFile(f *os.File, block bool) (bool, error) {
	flags := uint32(windows.LOCKFILE_EXCLUSIVE_LOCK)
	if !block {
		flags |= windows.LOCKFILE_FAIL_IMMEDIATELY
	}
	err := windows.LockFileEx(windows.Handle(f.Fd()), flags, 0, 1, 0, new(windows.Overlapped))
	if !block && errors.Is(err, windows.ERROR_LOCK_VIOLATION) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, nil
}

func unlockFile(f *os.File) {
	_ = windows.UnlockFileEx(windows.Handle(f.Fd()), 0, 1, 0, new(windows.Overlapped))
	_ = f.Close()
}

// lockStale is a no-op on Windows: an open handle prevents deletion, so a held
// lock file cannot have been unlinked or replaced beneath its holder.
func lockStale(*os.File, string) bool { return false }

// removeAllLocked releases before removing — Windows blocks deletion of files
// with open handles (our own included), and that same semantics is the guard:
// a resuming process's open handles make the RemoveAll fail and the reap skip.
func removeAllLocked(dir string, unlock func()) bool {
	unlock()
	return os.RemoveAll(dir) == nil
}
