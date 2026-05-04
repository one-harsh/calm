// Copyright 2026 The CALM Authors
// SPDX-License-Identifier: Apache-2.0

// Package exec runs local subprocesses (git, shell, build tools) on the
// developer's machine on behalf of the coding agent and captures stdout
// for ingestion. Only used by the calm-adapter binary; CALM itself never
// runs code (HLD §5 / §13).
package exec
