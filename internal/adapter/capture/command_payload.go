// Copyright 2026 The CALM Authors
// SPDX-License-Identifier: Apache-2.0

package capture

import (
	"strings"

	"github.com/one-harsh/calm/internal/adapter/exec"
)

// CommandPayload merges a command's captured stdout and stderr into the single
// visible / indexed payload — one construction shared by every shell, so the
// same command captures identical bytes regardless of integration. If a
// process wrote stderr (more than whitespace), it's part of the local result —
// no command-specific allowlist, no dropping diagnostics. Stream markers
// distinguish the sources when both are present so the LLM and source-scoped
// search can tell them apart.
func CommandPayload(r exec.Result) string {
	hasStdout := r.Stdout != ""
	hasStderr := strings.TrimSpace(r.Stderr) != ""
	switch {
	case hasStdout && hasStderr:
		sep := "\n"
		if !strings.HasSuffix(r.Stdout, "\n") {
			sep = "\n\n"
		}
		return "[stdout]\n" + r.Stdout + sep + "[stderr]\n" + r.Stderr
	case hasStderr:
		return "[stderr]\n" + r.Stderr
	default:
		return r.Stdout
	}
}
