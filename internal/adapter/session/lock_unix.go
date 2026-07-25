// Copyright 2026 The CALM Authors
// SPDX-License-Identifier: Apache-2.0

//go:build unix

package session

import (
	"errors"
	"os"
	"syscall"
)

func lockFile(f *os.File, block bool) (bool, error) {
	how := syscall.LOCK_EX
	if !block {
		how |= syscall.LOCK_NB
	}
	err := syscall.Flock(int(f.Fd()), how)
	if !block && errors.Is(err, syscall.EWOULDBLOCK) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, nil
}

func unlockFile(f *os.File) {
	_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
	_ = f.Close()
}

// lockStale reports whether the locked fd no longer names path: the lock file
// was unlinked or replaced (a GC reap raced this acquisition), so the held lock
// excludes nobody and must be re-acquired on the fresh inode.
func lockStale(f *os.File, path string) bool {
	fi, err := f.Stat()
	if err != nil {
		return true
	}
	pi, err := os.Stat(path)
	if err != nil {
		return true
	}
	fs, fok := fi.Sys().(*syscall.Stat_t)
	ps, pok := pi.Sys().(*syscall.Stat_t)
	if !fok || !pok {
		return false
	}
	return fs.Dev != ps.Dev || fs.Ino != ps.Ino
}

// removeAllLocked reaps while still holding the lock — flock does not prevent
// unlink, so releasing first would let a resuming process re-establish into the
// directory mid-delete.
func removeAllLocked(dir string, unlock func()) bool {
	ok := os.RemoveAll(dir) == nil
	unlock()
	return ok
}
