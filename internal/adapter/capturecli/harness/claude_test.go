// Copyright 2026 The CALM Authors
// SPDX-License-Identifier: Apache-2.0

package harness

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func marshal(t *testing.T, m map[string]any) []byte {
	t.Helper()
	b, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestParse(t *testing.T) {
	ev := Claude.Parse(marshal(t, map[string]any{
		"hook_event_name": "PreToolUse", "tool_name": "Bash", "session_id": "s",
		"tool_input": map[string]string{"command": "git status"},
	}))
	if ev.Kind != KindRewrite || ev.Rewrite.Command != "git status" || ev.Rewrite.SessionID != "s" {
		t.Errorf("PreToolUse/Bash → %+v; want KindRewrite", ev)
	}

	if ev := Claude.Parse(marshal(t, map[string]any{"hook_event_name": "PreToolUse", "tool_name": "Read"})); ev.Kind != KindPassThrough {
		t.Errorf("non-Bash rewrite → %v; want KindPassThrough", ev.Kind)
	}
	if ev := Claude.Parse(marshal(t, map[string]any{"hook_event_name": "PostToolUse", "tool_name": "Read"})); ev.Kind != KindPassThrough {
		t.Errorf("non-Bash observe → %v; want KindPassThrough", ev.Kind)
	}

	ev = Claude.Parse(marshal(t, map[string]any{
		"hook_event_name": "PostToolUse", "tool_name": "Bash", "session_id": "s",
		"tool_input":    map[string]string{"command": "echo hi"},
		"tool_response": map[string]any{"stdout": "hi", "isImage": false},
	}))
	if ev.Kind != KindObserve || !ev.Observe.CanReplace || ev.Observe.Stdout != "hi" {
		t.Errorf("PostToolUse success → %+v; want KindObserve stdout=hi CanReplace=true", ev.Observe)
	}

	ev = Claude.Parse(marshal(t, map[string]any{
		"hook_event_name": "PostToolUseFailure", "tool_name": "Bash", "session_id": "s",
		"tool_input": map[string]string{"command": "false"}, "error": "Exit code 3\nboom",
	}))
	if ev.Kind != KindObserve || ev.Observe.CanReplace || ev.Observe.ExitCode != 3 || ev.Observe.Stderr != "boom" {
		t.Errorf("failure → %+v; want KindObserve exit 3 stderr=boom CanReplace=false", ev.Observe)
	}

	for src, want := range map[string]Disposition{
		"startup": DispositionFreshCard, "clear": DispositionFreshCard,
		"compact": DispositionRefresherCard, "resume": DispositionNone,
	} {
		ev := Claude.Parse(marshal(t, map[string]any{"hook_event_name": "SessionStart", "source": src}))
		if ev.Kind != KindSessionStart || ev.SessionStart.Disposition != want {
			t.Errorf("SessionStart source=%s → %v; want %v", src, ev.SessionStart.Disposition, want)
		}
	}

	if ev := Claude.Parse([]byte("{ not json")); ev.Kind != KindPassThrough {
		t.Errorf("malformed → %v; want KindPassThrough", ev.Kind)
	}
	if ev := Claude.Parse(marshal(t, map[string]any{"hook_event_name": "PostToolBatch"})); ev.Kind != KindPassThrough {
		t.Errorf("unknown event → %v; want KindPassThrough", ev.Kind)
	}
}

// A delivery at the cap is flagged truncated on both success and failure events.
func TestParse_Truncation(t *testing.T) {
	atCap := strings.Repeat("x", claudeDeliveryCap)
	under := atCap[:claudeDeliveryCap-1]

	success := func(stdout string) ObserveEvent {
		p := payload{HookEventName: eventPostToolUse, ToolName: toolBash}
		p.ToolResponse.Stdout = stdout
		return p.observe()
	}
	if !success(atCap).Truncated {
		t.Error("success delivery at the cap must flag Truncated")
	}
	if success(under).Truncated {
		t.Error("success delivery under the cap must not flag Truncated")
	}

	failure := func(errStr string) ObserveEvent {
		return payload{HookEventName: eventPostToolUseFailure, ToolName: toolBash, Error: errStr}.observe()
	}
	if !failure("Exit code 3\n" + atCap).Truncated {
		t.Error("failure remainder at the cap must flag Truncated")
	}
	if failure("Exit code 3\n" + under).Truncated {
		t.Error("failure remainder under the cap must not flag Truncated")
	}
}

// The Bash replacement envelope must be exactly {stdout, stderr, interrupted, isImage}.
func TestRenderObserve_ObjectShape(t *testing.T) {
	var env struct {
		HookSpecificOutput struct {
			HookEventName     string                     `json:"hookEventName"`
			UpdatedToolOutput map[string]json.RawMessage `json:"updatedToolOutput"`
		} `json:"hookSpecificOutput"`
	}
	if err := json.Unmarshal(Claude.RenderObserve(ObserveResponse{Stdout: "body", Interrupted: true}), &env); err != nil {
		t.Fatalf("render not decodable: %v", err)
	}
	if env.HookSpecificOutput.HookEventName != "PostToolUse" {
		t.Errorf("hookEventName = %q; want PostToolUse", env.HookSpecificOutput.HookEventName)
	}
	keys := env.HookSpecificOutput.UpdatedToolOutput
	want := []string{"stdout", "stderr", "interrupted", "isImage"}
	if len(keys) != len(want) {
		t.Errorf("updatedToolOutput keys = %v; want exactly %v", keys, want)
	}
	for _, k := range want {
		if _, ok := keys[k]; !ok {
			t.Errorf("updatedToolOutput missing %q; got %v", k, keys)
		}
	}
}

func TestRenderRewrite(t *testing.T) {
	var env struct {
		HookSpecificOutput struct {
			HookEventName string `json:"hookEventName"`
			UpdatedInput  struct {
				Command     string `json:"command"`
				Description string `json:"description"`
			} `json:"updatedInput"`
		} `json:"hookSpecificOutput"`
	}
	if err := json.Unmarshal(Claude.RenderRewrite(RewriteResponse{Command: "wrapped", Description: "orig"}), &env); err != nil {
		t.Fatalf("render not decodable: %v", err)
	}
	o := env.HookSpecificOutput
	if o.HookEventName != "PreToolUse" || o.UpdatedInput.Command != "wrapped" || o.UpdatedInput.Description != "orig" {
		t.Errorf("rewrite envelope = %+v; want PreToolUse/wrapped/orig", o)
	}
}

// Only the hooks subtree counts; unparseable JSON falls back to the whole file.
func TestReferencesCaptureHook(t *testing.T) {
	cases := []struct {
		name string
		data string
		want bool
	}{
		{"hook-wired", `{"hooks":{"PreToolUse":[{"hooks":[{"command":"calm-capture hook"}]}]}}`, true},
		{"registration-only", `{"enabledPlugins":{"calm-capture@calm-capture":true},"extraKnownMarketplaces":{"calm-capture":{}}}`, false},
		{"no-hooks-key", `{"model":"opus"}`, false},
		{"empty-hooks", `{"hooks":{}}`, false},
		{"unparseable-with-binary", `{ not json — calm-capture hook`, true},
		{"unparseable-without-binary", `{ not json`, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := referencesCaptureHook([]byte(tc.data)); got != tc.want {
				t.Errorf("referencesCaptureHook(%s) = %v; want %v", tc.data, got, tc.want)
			}
		})
	}
}

func TestParseFailureError(t *testing.T) {
	cases := []struct {
		in       string
		wantCode int
		wantRest string
	}{
		{"Exit code 3\nout-line\nerr-line", 3, "out-line\nerr-line"},
		{"Exit code 5", 5, ""},                            // bare code, no output
		{"segfault", 1, "segfault"},                       // no prefix → exit 1, whole string
		{"Exit code notint\nx", 1, "Exit code notint\nx"}, // unreadable code → exit 1, whole string
	}
	for _, tc := range cases {
		code, rest := parseFailureError(tc.in)
		if code != tc.wantCode || rest != tc.wantRest {
			t.Errorf("parseFailureError(%q) = (%d, %q); want (%d, %q)", tc.in, code, rest, tc.wantCode, tc.wantRest)
		}
	}
}

// HooksJSON is the installed contract: the observation hook set, no PreToolUse.
func TestHooksJSON(t *testing.T) {
	cmd := "'/opt/bin/calm-capture' hook"
	var parsed struct {
		Hooks map[string][]struct {
			Matcher string `json:"matcher"`
			Hooks   []struct {
				Type    string `json:"type"`
				Command string `json:"command"`
				Timeout int    `json:"timeout"`
			} `json:"hooks"`
		} `json:"hooks"`
	}
	if err := json.Unmarshal(Claude.HooksJSON(cmd), &parsed); err != nil {
		t.Fatalf("hooks.json is not valid JSON: %v", err)
	}
	if _, ok := parsed.Hooks["PreToolUse"]; ok {
		t.Error("hooks.json must not install a PreToolUse rewrite layer")
	}
	for _, ev := range []string{"PostToolUse", "PostToolUseFailure"} {
		entries, ok := parsed.Hooks[ev]
		if !ok || len(entries) == 0 {
			t.Fatalf("hooks.json missing %q", ev)
		}
		h := entries[0]
		if h.Matcher != "Bash" {
			t.Errorf("%s matcher = %q; want Bash", ev, h.Matcher)
		}
		if h.Hooks[0].Command != cmd {
			t.Errorf("%s command = %q; want the hook command verbatim", ev, h.Hooks[0].Command)
		}
		if h.Hooks[0].Timeout != claudeHookTimeoutSeconds {
			t.Errorf("%s timeout = %d; want %d", ev, h.Hooks[0].Timeout, claudeHookTimeoutSeconds)
		}
	}
	ss, ok := parsed.Hooks["SessionStart"]
	if !ok || len(ss) == 0 {
		t.Fatal("hooks.json missing SessionStart")
	}
	if !strings.Contains(ss[0].Matcher, "startup") || !strings.Contains(ss[0].Matcher, "compact") {
		t.Errorf("SessionStart matcher = %q; want startup|…|compact", ss[0].Matcher)
	}
	if ss[0].Hooks[0].Command != cmd {
		t.Errorf("SessionStart command = %q; want the hook command verbatim", ss[0].Hooks[0].Command)
	}
}

func TestOtherHookLayers(t *testing.T) {
	home, cwd := t.TempDir(), t.TempDir()
	write := func(dir, name, content string) {
		d := filepath.Join(dir, ".claude")
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(d, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write(home, "settings.json", `{"hooks":{"PreToolUse":[{"hooks":[{"command":"calm-capture hook"}]}]}}`) // hit
	write(cwd, "settings.json", `{"enabledPlugins":{"calm-capture@calm-capture":true}}`)                   // registration-only miss
	write(cwd, "settings.local.json", `{ not json calm-capture hook`)                                      // unparseable fallback hit

	got := Claude.OtherHookLayers(home, cwd)
	want := []string{
		filepath.Join(home, ".claude", "settings.json"),
		filepath.Join(cwd, ".claude", "settings.local.json"),
	}
	if len(got) != len(want) {
		t.Fatalf("OtherHookLayers = %v; want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("OtherHookLayers[%d] = %q; want %q", i, got[i], want[i])
		}
	}
}
