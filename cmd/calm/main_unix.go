// Copyright 2026 The CALM Authors
// SPDX-License-Identifier: Apache-2.0

//go:build unix

package main

import (
	"fmt"
	"os"
	"syscall"
)

// Raw tokens live transiently in memory; a core dump would persist them to disk.
func disableCoreDumps() {
	if err := syscall.Setrlimit(syscall.RLIMIT_CORE, &syscall.Rlimit{Cur: 0, Max: 0}); err != nil {
		fmt.Fprintf(os.Stderr, "warn: setrlimit(RLIMIT_CORE, 0) failed: %v\n", err)
	}
}
