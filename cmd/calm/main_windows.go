// Copyright 2026 The CALM Authors
// SPDX-License-Identifier: Apache-2.0

//go:build windows

package main

// rlimit-based core suppression is a Unix mechanism; Windows crash-dump
// policy (Windows Error Reporting) is an operator setting outside the
// process, so the raw-token-in-core concern is handled there.
func disableCoreDumps() {}
