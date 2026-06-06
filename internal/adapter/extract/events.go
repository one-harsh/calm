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
)

// Event types and priorities mirror the HLD's example taxonomy — changing them is
// an HLD-level decision.
const (
	priorityToolInvocation = 3
	priorityErrorObserved  = 2
	priorityGitOperation   = 2
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
)

const maxTraceSnippet = 512

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
		s = s[len(s)-maxTraceSnippet:]
	}
	return strings.TrimSpace(s)
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
