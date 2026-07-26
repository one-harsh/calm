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
