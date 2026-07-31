// Copyright 2026 The CALM Authors
// SPDX-License-Identifier: Apache-2.0

package harness

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// Claude is the Claude Code harness.
var Claude claudeHarness

type claudeHarness struct{}

const (
	// claudeDeliveryCap: Claude hands hooks at most this many bytes of output, cut
	// silently mid-line, so a delivery at the cap is flagged truncated.
	claudeDeliveryCap        = 30000
	claudeFailureExitPrefix  = "Exit code "
	claudeHookTimeoutSeconds = 30
	binaryName               = "calm-capture"
)

const (
	eventPreToolUse         = "PreToolUse"
	eventPostToolUse        = "PostToolUse"
	eventPostToolUseFailure = "PostToolUseFailure"
	eventSessionStart       = "SessionStart"
	toolBash                = "Bash"

	sourceStartup = "startup"
	sourceClear   = "clear"
	sourceCompact = "compact"
)

type payload struct {
	HookEventName string `json:"hook_event_name"`
	SessionID     string `json:"session_id"`
	Source        string `json:"source"`
	CWD           string `json:"cwd"`
	ToolName      string `json:"tool_name"`
	ToolInput     struct {
		Command string `json:"command"`
	} `json:"tool_input"`
	ToolResponse struct {
		Stdout      string `json:"stdout"`
		Stderr      string `json:"stderr"`
		Interrupted bool   `json:"interrupted"`
		IsImage     bool   `json:"isImage"`
	} `json:"tool_response"`
	Error       string `json:"error"`
	IsInterrupt bool   `json:"is_interrupt"`
}

func (claudeHarness) Parse(stdin []byte) Event {
	var p payload
	if err := json.Unmarshal(stdin, &p); err != nil {
		return Event{Kind: KindPassThrough}
	}
	switch p.HookEventName {
	case eventPreToolUse:
		if p.ToolName != toolBash {
			return Event{Kind: KindPassThrough}
		}
		return Event{Kind: KindRewrite, Rewrite: RewriteEvent{SessionID: p.SessionID, Command: p.ToolInput.Command}}
	case eventPostToolUse, eventPostToolUseFailure:
		if p.ToolName != toolBash {
			return Event{Kind: KindPassThrough}
		}
		return Event{Kind: KindObserve, Observe: p.observe()}
	case eventSessionStart:
		return Event{Kind: KindSessionStart, SessionStart: SessionStartEvent{SessionID: p.SessionID, Disposition: disposition(p.Source)}}
	default:
		return Event{Kind: KindPassThrough}
	}
}

func (p payload) observe() ObserveEvent {
	failure := p.HookEventName == eventPostToolUseFailure
	ev := ObserveEvent{SessionID: p.SessionID, Command: p.ToolInput.Command, Cwd: p.CWD, IsImage: p.ToolResponse.IsImage, CanReplace: !failure}
	if !failure {
		ev.Stdout = p.ToolResponse.Stdout
		ev.Stderr = p.ToolResponse.Stderr
		ev.Interrupted = p.ToolResponse.Interrupted
		ev.Truncated = truncated(p.ToolResponse.Stdout)
		return ev
	}
	code, remainder := parseFailureError(p.Error)
	ev.ExitCode = code
	// The merged remainder goes to Stderr, not Stdout: extract sources a nonzero-exit
	// event's trace snippet from Stderr alone (fillErrorFacts → traceSnippet(Stderr)).
	ev.Stderr = remainder
	ev.Interrupted = p.IsInterrupt
	ev.Truncated = truncated(remainder)
	return ev
}

func truncated(s string) bool { return len(s) >= claudeDeliveryCap }

// parseFailureError splits a failure `error` string: `Exit code N` then the merged
// output. An unreadable first line defaults to exit 1 with the whole string kept.
func parseFailureError(errStr string) (int, string) {
	first, rest, _ := strings.Cut(errStr, "\n")
	if strings.HasPrefix(first, claudeFailureExitPrefix) {
		if n, err := strconv.Atoi(strings.TrimSpace(first[len(claudeFailureExitPrefix):])); err == nil {
			return n, rest
		}
	}
	return 1, errStr
}

func disposition(source string) Disposition {
	switch source {
	case sourceStartup, sourceClear:
		return DispositionFreshCard
	case sourceCompact:
		return DispositionRefresherCard
	default:
		return DispositionNone
	}
}

type updatedToolOutput struct {
	Stdout      string `json:"stdout"`
	Stderr      string `json:"stderr"`
	Interrupted bool   `json:"interrupted"`
	IsImage     bool   `json:"isImage"`
}

type postToolUseResponse struct {
	HookSpecificOutput struct {
		HookEventName     string            `json:"hookEventName"`
		UpdatedToolOutput updatedToolOutput `json:"updatedToolOutput"`
	} `json:"hookSpecificOutput"`
}

type updatedInput struct {
	Command     string `json:"command"`
	Description string `json:"description"`
}

type preToolUseResponse struct {
	HookSpecificOutput struct {
		HookEventName string       `json:"hookEventName"`
		UpdatedInput  updatedInput `json:"updatedInput"`
	} `json:"hookSpecificOutput"`
}

// RenderObserve emits the Bash object envelope; the harness rejects a bare string,
// so updatedToolOutput must be this object.
func (claudeHarness) RenderObserve(r ObserveResponse) []byte {
	var resp postToolUseResponse
	resp.HookSpecificOutput.HookEventName = eventPostToolUse
	resp.HookSpecificOutput.UpdatedToolOutput = updatedToolOutput{Stdout: r.Stdout, Interrupted: r.Interrupted}
	data, err := json.Marshal(resp)
	if err != nil {
		return nil
	}
	return data
}

func (claudeHarness) RenderRewrite(r RewriteResponse) []byte {
	var resp preToolUseResponse
	resp.HookSpecificOutput.HookEventName = eventPreToolUse
	resp.HookSpecificOutput.UpdatedInput = updatedInput(r)
	data, err := json.Marshal(resp)
	if err != nil {
		return nil
	}
	return data
}

// HooksJSON renders the plugin hook set. hookCommand is the already-quoted
// `'<binPath>' hook` invocation.
func (claudeHarness) HooksJSON(hookCommand string) []byte {
	cmd, _ := json.Marshal(hookCommand)
	return fmt.Appendf(nil, `{
  "hooks": {
    "PostToolUse": [
      { "matcher": "Bash", "hooks": [{ "type": "command", "command": %s, "timeout": %d }] }
    ],
    "PostToolUseFailure": [
      { "matcher": "Bash", "hooks": [{ "type": "command", "command": %s, "timeout": %d }] }
    ],
    "SessionStart": [
      { "matcher": "startup|resume|clear|compact", "hooks": [{ "type": "command", "command": %s }] }
    ]
  }
}
`, cmd, claudeHookTimeoutSeconds, cmd, claudeHookTimeoutSeconds, cmd)
}

// OtherHookLayers returns the settings files that reference calm-capture as a hook
// (AD07): a stacked capture layer corrupts capture identity. Warn-only.
func (claudeHarness) OtherHookLayers(home, cwd string) []string {
	var found []string
	for _, p := range settingsCandidates(home, cwd) {
		data, err := os.ReadFile(p) //nolint:gosec // operator's own settings, read to warn only
		if err != nil {
			continue
		}
		if referencesCaptureHook(data) {
			found = append(found, p)
		}
	}
	return found
}

func settingsCandidates(home, cwd string) []string {
	return []string{
		filepath.Join(home, ".claude", "settings.json"),
		filepath.Join(cwd, ".claude", "settings.json"),
		filepath.Join(cwd, ".claude", "settings.local.json"),
	}
}

// referencesCaptureHook counts a file only when its `hooks` subtree names
// calm-capture: the plugin's own marketplace/enabledPlugins entries name it
// elsewhere and must not self-warn. Unparseable JSON falls back to the whole file
// (AD07 prefers a false warning over a missed layer).
func referencesCaptureHook(data []byte) bool {
	var s struct {
		Hooks json.RawMessage `json:"hooks"`
	}
	if err := json.Unmarshal(data, &s); err != nil {
		return bytes.Contains(data, []byte(binaryName))
	}
	return bytes.Contains(s.Hooks, []byte(binaryName))
}
