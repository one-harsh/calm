// Copyright 2026 The CALM Authors
// SPDX-License-Identifier: Apache-2.0

package capturecli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/mock"

	"github.com/one-harsh/calm/internal/adapter/calm"
	"github.com/one-harsh/calm/internal/adapter/extract"
)

func postToolUsePayload(t *testing.T, sessionID, command, stdout string) []byte {
	t.Helper()
	p := map[string]any{
		"hook_event_name": "PostToolUse",
		"tool_name":       "Bash",
		"cwd":             "/work",
		"tool_input":      map[string]string{"command": command},
		"tool_response": map[string]any{
			"stdout": stdout, "stderr": "", "interrupted": false, "isImage": false,
		},
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

func decodeObservation(t *testing.T, out string) (name, stdout, stderr string) {
	t.Helper()
	var env struct {
		HookSpecificOutput struct {
			HookEventName     string `json:"hookEventName"`
			UpdatedToolOutput struct {
				Stdout string `json:"stdout"`
				Stderr string `json:"stderr"`
			} `json:"updatedToolOutput"`
		} `json:"hookSpecificOutput"`
	}
	if err := json.Unmarshal([]byte(out), &env); err != nil {
		t.Fatalf("response is not an observation envelope: %v\n%s", err, out)
	}
	o := env.HookSpecificOutput
	return o.HookEventName, o.UpdatedToolOutput.Stdout, o.UpdatedToolOutput.Stderr
}

// An executed PostToolUse/Bash result is captured and the raw result replaced
// with the engine's presentation.
func TestHook_PostToolUse_CaptureAndReplace(t *testing.T) {
	mc := calm.NewMockClient(t)
	expectEstablish(mc, "tok-obs")
	d, _, _ := newDeps(t, mc)

	out, code := dispatchHook(t, d, loadHookFixture(t, "claude_posttooluse_bash.json"))
	if code != 0 {
		t.Fatalf("exit = %d; want 0", code)
	}
	name, stdout, stderr := decodeObservation(t, out)
	if name != "PostToolUse" {
		t.Errorf("hookEventName = %q; want PostToolUse", name)
	}
	if !strings.Contains(stdout, "↳ source=") {
		t.Errorf("replacement stdout must carry the recall trailer; got:\n%s", stdout)
	}
	if !strings.Contains(stdout, "AI28-marker-x7q4z") {
		t.Errorf("replacement must present the original output verbatim; got:\n%s", stdout)
	}
	if stderr != "" {
		t.Errorf("replacement stderr must be empty; got %q", stderr)
	}
	wantHint := recallFor(hookBinPath(), "00000000-0000-0000-0000-000000000003")
	if !strings.Contains(stdout, wantHint) {
		t.Errorf("replacement must embed the self-locating recall hint %q; got:\n%s", wantHint, stdout)
	}
}

// never-worse: a failed capture leaves the native result standing.
func TestHook_PostToolUse_DegradedEmitsNothing(t *testing.T) {
	mc := calm.NewMockClient(t)
	mc.EXPECT().RegisterClient(mock.Anything, mock.Anything).Return(true, nil).Maybe()
	mc.EXPECT().CreateSession(mock.Anything, mock.Anything, mock.Anything, mock.Anything).Return("tok", nil).Once()
	mc.EXPECT().Ingest(mock.Anything, mock.Anything, mock.Anything).
		Return(calm.IngestSummary{}, errors.New("ingest boom")).Maybe()
	mc.EXPECT().WriteEvents(mock.Anything, mock.Anything, mock.Anything).Return(nil).Maybe()
	d, _, _ := newDeps(t, mc)

	out, code := dispatchHook(t, d, loadHookFixture(t, "claude_posttooluse_bash.json"))
	if code != 0 || out != "" {
		t.Errorf("a failed capture must emit nothing; got code=%d out=%q", code, out)
	}
}

func TestHookDegraded_PostToolUse_PassThrough(t *testing.T) {
	out := &bytes.Buffer{}
	code := HookDegraded(context.Background(), bytes.NewReader(loadHookFixture(t, "claude_posttooluse_bash.json")), out)
	if code != 0 || out.Len() != 0 {
		t.Errorf("degraded observation must pass through empty; got code=%d out=%q", code, out.String())
	}
}

func TestHook_PostToolUse_IsImage_PassThrough(t *testing.T) {
	d, _, _ := newDeps(t, calm.NewMockClient(t))
	payload := `{"session_id":"s1","cwd":"/work","hook_event_name":"PostToolUse","tool_name":"Bash","tool_input":{"command":"cat img.png"},"tool_response":{"stdout":"data","stderr":"","interrupted":false,"isImage":true}}`
	out, code := dispatchHook(t, d, []byte(payload))
	if code != 0 || out != "" {
		t.Errorf("an image result must pass through empty; got code=%d out=%q", code, out)
	}
}

func TestHook_PostToolUse_MissingSessionID_PassThrough(t *testing.T) {
	d, _, _ := newDeps(t, calm.NewMockClient(t))
	out, code := dispatchHook(t, d, postToolUsePayload(t, "", "git status", "ok"))
	if code != 0 || out != "" {
		t.Errorf("missing session_id must pass through empty; got code=%d out=%q", code, out)
	}
}

// AD07: an already-executed calm-capture command must not be re-captured.
func TestHook_PostToolUse_CalmCaptureItself_PassThrough(t *testing.T) {
	d, _, _ := newDeps(t, calm.NewMockClient(t))
	out, code := dispatchHook(t, d, postToolUsePayload(t, "s1", "calm-capture search marker", "hits"))
	if code != 0 || out != "" {
		t.Errorf("a calm-capture command must pass through empty (AD07); got code=%d out=%q", code, out)
	}
}

// AD07: calm-capture piped into another command still passes through.
func TestHook_PostToolUse_PipedCalmCapture_PassThrough(t *testing.T) {
	d, _, _ := newDeps(t, calm.NewMockClient(t))
	cmd := `'/opt/bin/calm-capture' search --session 'x' "seq" 2>&1 | head -50`
	out, code := dispatchHook(t, d, postToolUsePayload(t, "s1", cmd, "seq output"))
	if code != 0 || out != "" {
		t.Errorf("piped calm-capture must pass through empty; got code=%d out=%q", code, out)
	}
}

// The failure event is capture-only: it indexes the merged output and emits nothing.
func TestHook_PostToolUseFailure_CapturesWithoutReplacement(t *testing.T) {
	var gotContent string
	var gotEvents []calm.EventInput
	mc := calm.NewMockClient(t)
	mc.EXPECT().RegisterClient(mock.Anything, mock.Anything).Return(true, nil).Maybe()
	mc.EXPECT().CreateSession(mock.Anything, mock.Anything, mock.Anything, mock.Anything).Return("tok-fail", nil).Once()
	mc.EXPECT().Ingest(mock.Anything, "tok-fail", mock.Anything).
		Run(func(_ context.Context, _ string, in calm.IngestInput) { gotContent = in.Content }).
		Return(calm.IngestSummary{Source: "calm:v1:test", SectionsIndexed: 1, SectionsTotal: 1}, nil).Maybe()
	mc.EXPECT().WriteEvents(mock.Anything, "tok-fail", mock.Anything).
		Run(func(_ context.Context, _ string, events []calm.EventInput) { gotEvents = append(gotEvents, events...) }).
		Return(nil).Maybe()
	d, _, _ := newDeps(t, mc)

	out, code := dispatchHook(t, d, loadHookFixture(t, "claude_posttoolusefailure_bash.json"))
	if code != 0 {
		t.Fatalf("exit = %d; want 0", code)
	}
	if out != "" {
		t.Errorf("the failure event is capture-only; stdout must be empty, got:\n%s", out)
	}
	if !strings.Contains(gotContent, "out-line") || !strings.Contains(gotContent, "err-line") {
		t.Errorf("ingested payload must carry the merged failure output; got %q", gotContent)
	}
	var errEvent *calm.EventInput
	for i := range gotEvents {
		if gotEvents[i].Type == extract.EventErrorObserved {
			errEvent = &gotEvents[i]
		}
	}
	if errEvent == nil {
		t.Fatalf("a nonzero-exit capture must derive an error_observed event; got %+v", gotEvents)
	}
	// The spool round-trips events through JSON, so a number arrives as float64.
	if got, _ := errEvent.Data["exit_code"].(float64); got != 3 {
		t.Errorf("error event exit_code = %v; want 3", errEvent.Data["exit_code"])
	}
	// The remainder is placed in Stderr so the trace snippet derives from it.
	if snip, _ := errEvent.Data["trace_snippet"].(string); !strings.Contains(snip, "err-line") {
		t.Errorf("error event trace_snippet must come from the stderr-placed remainder; got %q", snip)
	}
}

// AD07: a stacked user-scope calm-capture layer is re-checked at session start.
func TestHook_SessionStart_WarnsOnOtherCaptureLayer(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	claudeDir := filepath.Join(home, ".claude")
	if err := os.MkdirAll(claudeDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(claudeDir, "settings.json"),
		[]byte(`{"hooks":{"PreToolUse":[{"matcher":"Bash","hooks":[{"type":"command","command":"calm-capture hook"}]}]}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	d, _, _ := newDeps(t, calm.NewMockClient(t))

	out, code := dispatchHook(t, d, loadHookFixture(t, "claude_sessionstart_startup.json"))
	if code != 0 {
		t.Fatalf("exit = %d; want 0", code)
	}
	if !strings.Contains(out, "already references calm-capture") {
		t.Errorf("SessionStart must warn about the stacked capture layer; got:\n%s", out)
	}
	if !strings.Contains(out, cardMarker) {
		t.Errorf("the card must still render; got:\n%s", out)
	}
}

// The plugin's own registration names calm-capture outside the hooks subtree and
// must not self-warn.
func TestHook_SessionStart_NoWarnOnPluginRegistration(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	claudeDir := filepath.Join(home, ".claude")
	if err := os.MkdirAll(claudeDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(claudeDir, "settings.json"),
		[]byte(`{"enabledPlugins":{"calm-capture@calm-capture":true},"extraKnownMarketplaces":{"calm-capture":{"source":{"source":"local","path":"/x"}}}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	d, _, _ := newDeps(t, calm.NewMockClient(t))

	out, code := dispatchHook(t, d, loadHookFixture(t, "claude_sessionstart_startup.json"))
	if code != 0 {
		t.Fatalf("exit = %d; want 0", code)
	}
	if strings.Contains(out, "already references calm-capture") {
		t.Errorf("the plugin's own registration must not self-warn; got:\n%s", out)
	}
	if !strings.Contains(out, cardMarker) {
		t.Errorf("the card must still render; got:\n%s", out)
	}
}

func TestRecallFor(t *testing.T) {
	got := recallFor("/opt/with space/calm-capture", "conv 42")
	want := "'/opt/with space/calm-capture' search --session 'conv 42'"
	if got != want {
		t.Errorf("recallFor = %q; want %q", got, want)
	}
}
