// Copyright 2026 The CALM Authors
// SPDX-License-Identifier: Apache-2.0

package capturecli

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	osexec "os/exec"
	"path/filepath"
	"strings"
	"testing"

	logging "github.com/one-harsh/context-logging"

	"github.com/one-harsh/calm/internal/adapter/calm"
)

func loadHookFixture(t *testing.T, name string) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", "hook", name))
	if err != nil {
		t.Fatalf("read fixture %s: %v", name, err)
	}
	return data
}

// dispatchHook drives the `hook` subcommand with input on stdin and returns the
// stdout response and exit code.
func dispatchHook(t *testing.T, d Deps, input []byte) (string, int) {
	t.Helper()
	d.Stdin = bytes.NewReader(input)
	out := &bytes.Buffer{}
	d.Stdout = out
	code := Dispatch(context.Background(), d, []string{"hook"})
	return out.String(), code
}

func decodeRewrite(t *testing.T, out string) (command, description string) {
	t.Helper()
	var resp struct {
		HookSpecificOutput struct {
			HookEventName string `json:"hookEventName"`
			UpdatedInput  struct {
				Command     string `json:"command"`
				Description string `json:"description"`
			} `json:"updatedInput"`
		} `json:"hookSpecificOutput"`
	}
	if err := json.Unmarshal([]byte(out), &resp); err != nil {
		t.Fatalf("response is not a rewrite JSON: %v\n%s", err, out)
	}
	if resp.HookSpecificOutput.HookEventName != "PreToolUse" {
		t.Errorf("hookEventName = %q; want PreToolUse", resp.HookSpecificOutput.HookEventName)
	}
	return resp.HookSpecificOutput.UpdatedInput.Command, resp.HookSpecificOutput.UpdatedInput.Description
}

func bashPayload(t *testing.T, sessionID, command string) []byte {
	t.Helper()
	p := map[string]any{
		"hook_event_name": "PreToolUse",
		"tool_name":       "Bash",
		"tool_input":      map[string]string{"command": command},
	}
	if sessionID != "" {
		p["session_id"] = sessionID
	}
	b, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// The headline path: a real PreToolUse/Bash payload rewrites into a wrapped
// `exec` invocation whose description still names the original command.
func TestHook_PreToolUse_RewriteHappyPath(t *testing.T) {
	d, _, _ := newDeps(t, calm.NewMockClient(t))
	out, code := dispatchHook(t, d, loadHookFixture(t, "claude_pretooluse_bash.json"))
	if code != 0 {
		t.Fatalf("exit = %d; want 0", code)
	}
	command, description := decodeRewrite(t, out)
	if !strings.Contains(command, "exec --session ") || !strings.Contains(command, " -- 'git status'") {
		t.Errorf("rewrite must wrap the command in a single-string exec; got %q", command)
	}
	if description != "git status" {
		t.Errorf("description = %q; want the original command", description)
	}
}

// The rewrite embeds the payload's session_id so the wrapped capture lands in
// the conversation's own on-disk session.
func TestHook_PreToolUse_CarriesSessionID(t *testing.T) {
	d, _, _ := newDeps(t, calm.NewMockClient(t))
	out, _ := dispatchHook(t, d, loadHookFixture(t, "claude_pretooluse_bash.json"))
	command, _ := decodeRewrite(t, out)
	if !strings.Contains(command, "exec --session '00000000-0000-0000-0000-000000000001' -- ") {
		t.Errorf("rewrite must scope to the payload session_id; got %q", command)
	}
}

// The rewrite string is reparsed by the shell, so escaping must be POSIX-exact:
// the original command must survive embedded quotes, newlines and operators.
func TestHook_PreToolUse_EscapesCommand(t *testing.T) {
	d, _, _ := newDeps(t, calm.NewMockClient(t))
	cmd := "grep 'a b' f && echo \"x\"\nnext"
	out, code := dispatchHook(t, d, bashPayload(t, "s1", cmd))
	if code != 0 {
		t.Fatalf("exit = %d; want 0", code)
	}
	command, description := decodeRewrite(t, out)
	if !strings.HasSuffix(command, " -- "+shellSingleQuote(cmd)) {
		t.Errorf("rewrite must carry the POSIX-escaped original; got %q", command)
	}
	if description != cmd {
		t.Errorf("description must be the verbatim original; got %q", description)
	}
}

// AD07: a command already invoking calm-capture passes through unchanged, or the
// outer wrap would capture the inner wrapper's presentation.
func TestHook_PreToolUse_AlreadyWrapped_PassThrough(t *testing.T) {
	d, _, _ := newDeps(t, calm.NewMockClient(t))
	out, code := dispatchHook(t, d, bashPayload(t, "s1", "calm-capture exec -- 'git status'"))
	if code != 0 || out != "" {
		t.Errorf("already-wrapped must pass through empty; got code=%d out=%q", code, out)
	}
}

func TestHook_PreToolUse_NonBash_PassThrough(t *testing.T) {
	d, _, _ := newDeps(t, calm.NewMockClient(t))
	payload := `{"session_id":"s1","hook_event_name":"PreToolUse","tool_name":"Read","tool_input":{"command":"x"}}`
	out, code := dispatchHook(t, d, []byte(payload))
	if code != 0 || out != "" {
		t.Errorf("non-Bash tool must pass through empty; got code=%d out=%q", code, out)
	}
}

func TestHook_PreToolUse_MissingSessionID_PassThrough(t *testing.T) {
	d, _, _ := newDeps(t, calm.NewMockClient(t))
	out, code := dispatchHook(t, d, bashPayload(t, "", "git status"))
	if code != 0 || out != "" {
		t.Errorf("missing session_id must pass through empty; got code=%d out=%q", code, out)
	}
}

// AD07: calm-capture piped or chained (which Program collapses to "sh") is still
// recognized, so a plumbed retrieval is never rewritten and re-captured.
func TestHook_PreToolUse_PipedCalmCapture_PassThrough(t *testing.T) {
	d, _, _ := newDeps(t, calm.NewMockClient(t))
	cmd := `'/opt/bin/calm-capture' search --session 'x' "seq" 2>&1 | head -50`
	out, code := dispatchHook(t, d, bashPayload(t, "s1", cmd))
	if code != 0 || out != "" {
		t.Errorf("piped calm-capture must pass through empty; got code=%d out=%q", code, out)
	}
}

func TestHook_MalformedAndUnknown_PassThrough(t *testing.T) {
	d, _, _ := newDeps(t, calm.NewMockClient(t))
	for _, in := range []string{
		`{ not json`,
		`{"hook_event_name":"PostToolBatch","tool_name":"Bash","tool_input":{"command":"x"}}`,
		``,
	} {
		out, code := dispatchHook(t, d, []byte(in))
		if code != 0 || out != "" {
			t.Errorf("input %q must pass through empty; got code=%d out=%q", in, code, out)
		}
	}
}

// SessionStart is source-shaped: startup/clear teach over a fresh context,
// compact supersedes an earlier card, resume/fork stay silent.
func TestHook_SessionStart_PerSource(t *testing.T) {
	full := []string{"claude_sessionstart_startup.json", "claude_sessionstart_clear.json"}
	for _, f := range full {
		d, _, _ := newDeps(t, calm.NewMockClient(t))
		out, code := dispatchHook(t, d, loadHookFixture(t, f))
		if code != 0 {
			t.Fatalf("%s exit = %d; want 0", f, code)
		}
		if !strings.Contains(out, "session-start hook") || !strings.Contains(out, cardMarker) {
			t.Errorf("%s must inject the teaching card; got:\n%s", f, out)
		}
		if strings.Contains(out, "replaces any earlier") {
			t.Errorf("%s must not use the refresher phrasing; got:\n%s", f, out)
		}
	}

	d, _, _ := newDeps(t, calm.NewMockClient(t))
	out, code := dispatchHook(t, d, loadHookFixture(t, "claude_sessionstart_compact.json"))
	if code != 0 || !strings.Contains(out, "replaces any earlier CALM capture inventory above") {
		t.Errorf("compact must inject the superseding refresher; got code=%d:\n%s", code, out)
	}

	d, _, _ = newDeps(t, calm.NewMockClient(t))
	out, code = dispatchHook(t, d, loadHookFixture(t, "claude_sessionstart_resume.json"))
	if code != 0 || out != "" {
		t.Errorf("resume must stay silent; got code=%d out=%q", code, out)
	}
}

// A fresh session (empty registry) gets the static teaching card with no
// inventory tail — never a stale one.
func TestHook_SessionStart_EmptyRegistry_StaticOnly(t *testing.T) {
	d, _, _ := newDeps(t, calm.NewMockClient(t))
	out, _ := dispatchHook(t, d, loadHookFixture(t, "claude_sessionstart_startup.json"))
	if strings.Contains(out, "captured so far (retrieve") {
		t.Errorf("empty registry must render no inventory; got:\n%s", out)
	}
}

// HookDegraded is the bootstrap-failure floor: with no root/logger it still
// rewrites Bash and still passes malformed input through, exit 0.
func TestHookDegraded_RewritesAndPassesThrough(t *testing.T) {
	out := &bytes.Buffer{}
	if code := HookDegraded(context.Background(), bytes.NewReader(bashPayload(t, "s1", "git status")), out); code != 0 {
		t.Fatalf("degraded exit = %d; want 0", code)
	}
	if !strings.Contains(out.String(), " -- 'git status'") {
		t.Errorf("degraded hook must still rewrite; got %q", out.String())
	}

	out.Reset()
	if code := HookDegraded(context.Background(), bytes.NewReader([]byte("{bad")), out); code != 0 || out.Len() != 0 {
		t.Errorf("degraded malformed must pass through empty; got code=%d out=%q", code, out.String())
	}
}

// A bootstrap-degraded hook can neither capture nor search, so SessionStart
// emits nothing — a card claiming those work would be a never-worse lie.
func TestHookDegraded_SessionStart_EmitsNothing(t *testing.T) {
	out := &bytes.Buffer{}
	code := HookDegraded(context.Background(), bytes.NewReader(loadHookFixture(t, "claude_sessionstart_startup.json")), out)
	if code != 0 || out.Len() != 0 {
		t.Errorf("degraded SessionStart must emit nothing; got code=%d out=%q", code, out.String())
	}
}

// The rewritten command is reparsed by the real shell, so shellSingleQuote must
// round-trip every representative form through `sh -c` byte-for-byte.
func TestShellSingleQuote_RoundTripThroughSh(t *testing.T) {
	for _, in := range []string{
		"git status",
		`echo "hi there"`,
		"has a ' quote",
		"a && b || c; d | e",
		"line1\nline2",
		"",
		"weird $VAR and `backtick`",
	} {
		out, err := osexec.Command("sh", "-c", "printf %s "+shellSingleQuote(in)).Output()
		if err != nil {
			t.Fatalf("sh -c for %q: %v", in, err)
		}
		if string(out) != in {
			t.Errorf("round-trip %q → %q", in, string(out))
		}
	}
}

// A command that merely names calm-capture (grep, cat) is not a wrapper
// invocation and must still be captured — the guard keys on program identity.
func TestHook_PreToolUse_MentionsBinaryName_StillCaptures(t *testing.T) {
	d, _, _ := newDeps(t, calm.NewMockClient(t))
	out, code := dispatchHook(t, d, bashPayload(t, "s1", "grep -r calm-capture docs/"))
	if code != 0 {
		t.Fatalf("exit = %d; want 0", code)
	}
	command, description := decodeRewrite(t, out)
	if !strings.Contains(command, "exec --session ") || description != "grep -r calm-capture docs/" {
		t.Errorf("a command that only names calm-capture must be wrapped; got %q", command)
	}
}

// A binary path with spaces must be single-quoted in the rewrite, or the shell
// that reparses it splits argv[0] and the wrapped command fails (never-worse).
func TestHook_PreToolUse_QuotesBinaryPath(t *testing.T) {
	binPath := "/opt/with space/calm-capture"
	out := &bytes.Buffer{}
	code := runHook(context.Background(), hookConfig{
		stdin:   bytes.NewReader(bashPayload(t, "s1", "git status")),
		stdout:  out,
		logger:  logging.Nop(),
		binPath: binPath,
	})
	if code != 0 {
		t.Fatalf("exit = %d; want 0", code)
	}
	command, _ := decodeRewrite(t, out.String())
	if !strings.HasPrefix(command, shellSingleQuote(binPath)+" exec ") {
		t.Errorf("rewrite must quote a spacey binary path; got %q", command)
	}
}
