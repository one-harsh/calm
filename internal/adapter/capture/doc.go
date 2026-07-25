// Copyright 2026 The CALM Authors
// SPDX-License-Identifier: Apache-2.0

// Package capture is the adapter's capture engine: the tool-agnostic pipeline
// that turns one executed local action into CALM-managed context — session
// pre-checks, staleness-token mint, preservation-first dual-write ingest,
// token recording, presentation-mode selection, and best-effort event
// emission — plus the source-token registry and degraded-reason classification
// every shell consumes whole (DESIGN.md Part I).
//
// The engine depends on no shell: it reaches the shell's per-session state
// through the capture.Session seam and returns a capture.Outcome the shell maps
// onto its own transport. A shell may import this package; this package must
// never import a shell.
package capture
