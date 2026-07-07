// Copyright 2026 The CALM Authors
// SPDX-License-Identifier: Apache-2.0

package extract

import (
	"fmt"
	"regexp"
	"strings"
	"unicode/utf8"
)

const toolName = "calm_run_command"

const (
	EventToolInvocation = "tool_invocation"
	EventErrorObserved  = "error_observed"
	EventGitOperation   = "git_operation"
	EventFileTouched    = "file_touched"
)

// Event types and priorities mirror the HLD's example taxonomy — changing them is
// an HLD-level decision.
const (
	priorityToolInvocation = 3
	priorityErrorObserved  = 2
	priorityGitOperation   = 2
	priorityFileTouched    = 1
)

const (
	keyToolName      = "tool_name"
	keyCommand       = "command"
	keyExitCode      = "exit_code"
	keyInvocationID  = "invocation_id"
	keyLatestSource  = "latest_source"
	keyHistorySource = "history_source"
	keyMessage       = "message"
	keySource        = "source"
	keyTraceSnippet  = "trace_snippet"
	keySubcommand    = "subcommand"
	keyPath          = "path"
	keyOperation     = "operation"
	keyDiff          = "diff"
	keyDiffTruncated = "diff_truncated"
)

// FileOperation is the file_touched operation enum per LABELING.md §5.
type FileOperation string

const (
	OperationEdit   FileOperation = "edit"
	OperationWrite  FileOperation = "write"
	OperationCreate FileOperation = "create"
)

const maxTraceSnippet = 512

// maxDiffBytes bounds the file_touched diff payload. Larger than
// maxTraceSnippet because the diff IS the payload's point — 512 would gut
// most multi-hunk edits, while 2KiB keeps typical edits whole and bounds a
// full-file rewrite.
const maxDiffBytes = 2048

// commandSummary is program + subcommand only — never the raw arg string, so an
// argument carrying a secret is never persisted.
func commandSummary(c cmd) string {
	if c.subcommand != "" {
		return c.program + " " + c.subcommand
	}
	return c.program
}

func errorMessage(r ExecResult) string {
	if r.TimedOut {
		return "command timed out"
	}
	return fmt.Sprintf("command exited with code %d", r.ExitCode)
}

// Events are persisted and audit-logged, so the stderr tail is bounded and sanitized.
func traceSnippet(stderr string) string {
	s := strings.TrimSpace(stderr)
	if s == "" {
		return ""
	}
	// Redact before truncating: a credential longer than maxTraceSnippet could
	// otherwise keep its tail inside the window while its marker (Bearer / --token=)
	// falls outside it, defeating the regex and persisting raw secret material.
	s = redactSecrets(sanitizeText(s))
	if len(s) > maxTraceSnippet {
		// Byte-slice the tail, then drop a leading partial codepoint the slice may split.
		s = strings.ToValidUTF8(s[len(s)-maxTraceSnippet:], "")
	}
	return strings.TrimSpace(s)
}

// sanitizeDiff applies LABELING.md §7 event-metadata discipline to a unified
// diff: redact before truncating (same reasoning as traceSnippet), strip
// control bytes / invalid UTF-8, then HEAD-truncate at maxDiffBytes — the
// opposite of traceSnippet's tail-keep, because a diff's signal lives at the
// head (file header + first hunks) while stderr's lives at the tail.
func sanitizeDiff(diff string) (string, bool) {
	s := strings.TrimSpace(diff)
	if s == "" {
		return "", false
	}
	s = redactSecrets(sanitizeText(s))
	truncated := false
	if len(s) > maxDiffBytes {
		s = strings.ToValidUTF8(s[:maxDiffBytes], "")
		truncated = true
	}
	return strings.TrimSpace(s), truncated
}

func sanitizeText(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r == utf8.RuneError:
			continue
		case r == '\n' || r == '\t':
			b.WriteRune(r)
		case r < 0x20 || r == 0x7f:
			continue
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

var secretPatterns = []struct {
	re   *regexp.Regexp
	repl string
}{
	{regexp.MustCompile(`(?i)\b(authorization:\s*bearer|bearer)\s+\S+`), "$1 <redacted>"},
	{regexp.MustCompile(`(?i)(--?(?:password|passwd|token|secret|api[_-]?key))(?:=|\s+)\S+`), "$1=<redacted>"},
}

func redactSecrets(s string) string {
	for _, p := range secretPatterns {
		s = p.re.ReplaceAllString(s, p.repl)
	}
	return s
}
