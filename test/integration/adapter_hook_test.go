// Copyright 2026 The CALM Authors
// SPDX-License-Identifier: Apache-2.0

package integration

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	logging "github.com/one-harsh/context-logging"

	"github.com/one-harsh/calm/internal/adapter/calm"
	"github.com/one-harsh/calm/internal/adapter/capturecli"
	"github.com/one-harsh/calm/internal/adapter/config"
	"github.com/one-harsh/calm/internal/adapter/obs"
	"github.com/one-harsh/calm/internal/secrets"
)

// hookBase builds a calm-capture Deps wired to real CALM under a private state
// root, so the hook/exec/search loop runs end-to-end without the harness.
func hookBase(t *testing.T, client calm.Client) capturecli.Deps {
	t.Helper()
	return capturecli.Deps{
		Cfg: config.Config{Calm: config.CalmConfig{
			URL:               env.serverURL,
			Client:            "default",
			SessionTTLMinutes: testDefaultTTLMinutes,
		}},
		Logger: logging.Nop(),
		Client: client,
		Root:   t.TempDir(),
	}
}

func realCalmClient(t *testing.T) calm.Client {
	t.Helper()
	c, err := calm.NewGenapiClient(env.serverURL, testMasterKey, nil)
	if err != nil {
		t.Fatalf("NewGenapiClient: %v", err)
	}
	return c
}

func uniqueSessionID(t *testing.T) string {
	t.Helper()
	return fmt.Sprintf("hook-%d", time.Now().UnixNano())
}

func runCLI(t *testing.T, base capturecli.Deps, args ...string) (string, string, int) {
	t.Helper()
	out, errb := &bytes.Buffer{}, &bytes.Buffer{}
	d := base
	d.Stdout, d.Stderr = out, errb
	code := capturecli.Dispatch(context.Background(), d, args)
	return out.String(), errb.String(), code
}

func runHookCLI(t *testing.T, base capturecli.Deps, payload []byte) (string, int) {
	t.Helper()
	out := &bytes.Buffer{}
	d := base
	d.Stdin = bytes.NewReader(payload)
	d.Stdout = out
	code := capturecli.Dispatch(context.Background(), d, []string{"hook"})
	return out.String(), code
}

func pretoolusePayload(t *testing.T, sessionID, command string) []byte {
	t.Helper()
	b, err := json.Marshal(map[string]any{
		"session_id":      sessionID,
		"cwd":             "/work",
		"permission_mode": "default",
		"hook_event_name": "PreToolUse",
		"tool_name":       "Bash",
		"tool_input":      map[string]string{"command": command, "description": command},
		"tool_use_id":     "toolu_x",
	})
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func sessionStartPayload(t *testing.T, sessionID, source string) []byte {
	t.Helper()
	b, err := json.Marshal(map[string]any{
		"session_id":      sessionID,
		"hook_event_name": "SessionStart",
		"source":          source,
	})
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func rewriteCommand(t *testing.T, out string) string {
	t.Helper()
	var resp struct {
		HookSpecificOutput struct {
			UpdatedInput struct {
				Command string `json:"command"`
			} `json:"updatedInput"`
		} `json:"hookSpecificOutput"`
	}
	if err := json.Unmarshal([]byte(out), &resp); err != nil {
		t.Fatalf("hook did not emit a rewrite: %v\n%s", err, out)
	}
	return resp.HookSpecificOutput.UpdatedInput.Command
}

// execSource pulls the base source label (staleness suffix stripped) off the
// capture-label line the engine renders in every presentation body.
func execSource(t *testing.T, out string) string {
	t.Helper()
	const key = `under "`
	i := strings.LastIndex(out, key)
	if i < 0 {
		t.Fatalf("exec presentation carried no capture label:\n%s", out)
	}
	rest := out[i+len(key):]
	if cut := strings.IndexByte(rest, '"'); cut >= 0 {
		rest = rest[:cut]
	}
	if at := strings.LastIndex(rest, "@"); at >= 0 {
		rest = rest[:at]
	}
	return rest
}

func padCommand(marker string) string {
	// Push output past the inline threshold so the presentation advertises a
	// recall label.
	return "printf '%s' '" + marker + " " + strings.Repeat("pad ", 256) + "'"
}

func posttoolusePayload(t *testing.T, sessionID, cwd, stdout string) []byte {
	t.Helper()
	b, err := json.Marshal(map[string]any{
		"session_id":      sessionID,
		"cwd":             cwd,
		"permission_mode": "default",
		"hook_event_name": "PostToolUse",
		"tool_name":       "Bash",
		"tool_input":      map[string]string{"command": "printf observed-output", "description": "emit output"},
		"tool_response": map[string]any{
			"stdout": stdout, "stderr": "", "interrupted": false, "isImage": false, "noOutputExpected": false,
		},
		"tool_use_id": "toolu_obs",
	})
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func posttoolusefailurePayload(t *testing.T, sessionID, cwd, errStr string) []byte {
	t.Helper()
	b, err := json.Marshal(map[string]any{
		"session_id":      sessionID,
		"cwd":             cwd,
		"permission_mode": "default",
		"hook_event_name": "PostToolUseFailure",
		"tool_name":       "Bash",
		"tool_input":      map[string]string{"command": "sh -c 'exit 3'", "description": "fail"},
		"error":           errStr,
		"is_interrupt":    false,
		"tool_use_id":     "toolu_obsfail",
	})
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// observationStdout decodes a PostToolUse replacement envelope and returns its
// updatedToolOutput.stdout — the presentation the harness shows in place of the
// raw result.
func observationStdout(t *testing.T, out string) string {
	t.Helper()
	var resp struct {
		HookSpecificOutput struct {
			HookEventName     string `json:"hookEventName"`
			UpdatedToolOutput struct {
				Stdout string `json:"stdout"`
			} `json:"updatedToolOutput"`
		} `json:"hookSpecificOutput"`
	}
	if err := json.Unmarshal([]byte(out), &resp); err != nil {
		t.Fatalf("hook did not emit an observation envelope: %v\n%s", err, out)
	}
	if resp.HookSpecificOutput.HookEventName != "PostToolUse" {
		t.Fatalf("envelope hookEventName = %q; want PostToolUse\n%s", resp.HookSpecificOutput.HookEventName, out)
	}
	return resp.HookSpecificOutput.UpdatedToolOutput.Stdout
}

// A PostToolUse result is captured into CALM and the raw result is replaced with
// a labeled presentation; the original output is retrievable verbatim under that
// label, and the next SessionStart card lists the observed capture.
func TestPostToolUseObservationRoundTrip(t *testing.T) {
	base := hookBase(t, realCalmClient(t))
	sid := uniqueSessionID(t)
	marker := "obsroundtripmarker"
	stdout := marker + " " + strings.Repeat("pad ", 256)

	emitted, code := runHookCLI(t, base, posttoolusePayload(t, sid, "/work", stdout))
	if code != 0 {
		t.Fatalf("hook exit = %d; want 0", code)
	}
	source := execSource(t, observationStdout(t, emitted))

	found, _, code := runCLI(t, base, "search", "--session", sid, "source="+source)
	if code != 0 {
		t.Fatalf("search exit = %d; want 0", code)
	}
	if !strings.Contains(found, marker) {
		t.Fatalf("observed output not retrievable verbatim under %q:\n%s", source, found)
	}

	card, code := runHookCLI(t, base, sessionStartPayload(t, sid, "compact"))
	if code != 0 {
		t.Fatalf("SessionStart exit = %d; want 0", code)
	}
	if !strings.Contains(card, source) {
		t.Errorf("SessionStart card must list the observed capture %q; got:\n%s", source, card)
	}
}

// never-worse: with CALM unreachable, observation captures nothing and emits
// nothing — the native result stands, exit 0.
func TestPostToolUseNeverWorse(t *testing.T) {
	down, err := calm.NewGenapiClient("http://127.0.0.1:1", testMasterKey, nil)
	if err != nil {
		t.Fatalf("client: %v", err)
	}
	base := hookBase(t, down)
	out, code := runHookCLI(t, base, posttoolusePayload(t, uniqueSessionID(t), "/work", "downmarker"))
	if code != 0 || out != "" {
		t.Errorf("CALM-down observation must emit nothing at exit 0; got code=%d out=%q", code, out)
	}
}

// A PostToolUseFailure is capture-only: the hook emits nothing (the native error
// result stands), but the failing command's output is indexed and retrievable.
func TestPostToolUseFailureIndexed(t *testing.T) {
	base := hookBase(t, realCalmClient(t))
	sid := uniqueSessionID(t)
	marker := "obsfailmarker"
	errStr := "Exit code 3\n" + marker + " " + strings.Repeat("pad ", 256)

	out, code := runHookCLI(t, base, posttoolusefailurePayload(t, sid, "/work", errStr))
	if code != 0 || out != "" {
		t.Fatalf("failure observation must emit nothing at exit 0; got code=%d out=%q", code, out)
	}

	found, _, code := runCLI(t, base, "search", "--session", sid, marker)
	if code != 0 {
		t.Fatalf("search exit = %d; want 0", code)
	}
	if !strings.Contains(found, marker) {
		t.Fatalf("failing output not indexed/retrievable:\n%s", found)
	}
}

// The whole hook-arm value loop: a PreToolUse payload rewrites into a wrapped
// exec that captures against real CALM, the presentation carries the recall
// trailer, and the capture is retrievable via search.
func TestHookRewriteRoundTrip(t *testing.T) {
	base := hookBase(t, realCalmClient(t))
	sid := uniqueSessionID(t)
	marker := "hookroundtripmarker"
	command := padCommand(marker)

	rewritten, code := runHookCLI(t, base, pretoolusePayload(t, sid, command))
	if code != 0 {
		t.Fatalf("hook exit = %d; want 0", code)
	}
	rw := rewriteCommand(t, rewritten)
	if !strings.Contains(rw, "exec --session '"+sid+"' -- '") {
		t.Fatalf("rewrite must wrap the command as a single-string exec scoped to the session; got %q", rw)
	}

	out, _, code := runCLI(t, base, "exec", "--session", sid, "--", command)
	if code != 0 {
		t.Fatalf("wrapped exec exit = %d; want 0", code)
	}
	source := execSource(t, out)

	found, _, code := runCLI(t, base, "search", "--session", sid, marker)
	if code != 0 {
		t.Fatalf("search exit = %d; want 0", code)
	}
	if !strings.Contains(found, marker) {
		t.Fatalf("captured output not retrievable via search (source %q):\n%s", source, found)
	}
}

// The session-start card reflects the conversation's corpus: after seeding
// captures, SessionStart(compact) teaches recall and surfaces the seeded
// identities, while a fresh session gets the static card with no stale inventory.
func TestSessionStartCardReflectsCorpus(t *testing.T) {
	base := hookBase(t, realCalmClient(t))
	sid := uniqueSessionID(t)

	out1, _, _ := runCLI(t, base, "exec", "--session", sid, "--", padCommand("corpusalpha"))
	out2, _, _ := runCLI(t, base, "exec", "--session", sid, "--", padCommand("corpusbeta"))
	recent := execSource(t, out2)
	_ = execSource(t, out1)

	card, code := runHookCLI(t, base, sessionStartPayload(t, sid, "compact"))
	if code != 0 {
		t.Fatalf("SessionStart(compact) exit = %d; want 0", code)
	}
	if !strings.Contains(card, "search --session '"+sid+"'") {
		t.Errorf("card must teach the session-qualified recall; got:\n%s", card)
	}
	if !strings.Contains(card, recent) {
		t.Errorf("card must surface the seeded identity %q; got:\n%s", recent, card)
	}

	fresh, code := runHookCLI(t, base, sessionStartPayload(t, uniqueSessionID(t), "startup"))
	if code != 0 {
		t.Fatalf("fresh SessionStart exit = %d; want 0", code)
	}
	if strings.Contains(fresh, recent) || strings.Contains(fresh, "captured so far (retrieve") {
		t.Errorf("a fresh session must not carry a stale inventory; got:\n%s", fresh)
	}
}

// never-worse: a malformed payload still passes through exit 0; a wrapped exec
// with CALM unreachable still returns the raw output plus the degradation
// sentence and the verbatim wrapped exit code.
func TestHookNeverWorse(t *testing.T) {
	base := hookBase(t, realCalmClient(t))
	if out, code := runHookCLI(t, base, []byte("{ not a payload")); code != 0 || out != "" {
		t.Errorf("malformed payload must pass through empty; got code=%d out=%q", code, out)
	}

	down, err := calm.NewGenapiClient("http://127.0.0.1:1", testMasterKey, nil)
	if err != nil {
		t.Fatalf("client: %v", err)
	}
	dead := hookBase(t, down)
	out, _, code := runCLI(t, dead, "exec", "--session", uniqueSessionID(t), "--", "printf 'downmarker'; exit 7")
	if code != 7 {
		t.Errorf("wrapped exit code must propagate verbatim; got %d", code)
	}
	if !strings.Contains(out, "downmarker") || !strings.Contains(out, obs.DegradedPhrase(obs.DegradedReasonCalmUnreachable)) {
		t.Errorf("CALM-down exec must show raw output plus the degradation sentence; got:\n%s", out)
	}
}

// AD07 both guards: a command already invoking calm-capture passes through the
// hook unmodified, and a nested wrapped exec (sentinel set) prints once.
func TestAlreadyWrappedNoDoubleCapture(t *testing.T) {
	base := hookBase(t, realCalmClient(t))
	out, code := runHookCLI(t, base, pretoolusePayload(t, uniqueSessionID(t), "calm-capture exec -- 'git status'"))
	if code != 0 || out != "" {
		t.Errorf("already-wrapped payload must pass through empty; got code=%d out=%q", code, out)
	}

	t.Setenv(capturecli.CaptureActiveEnv, "1")
	nested, _, code := runCLI(t, base, "exec", "--session", uniqueSessionID(t), "--", "printf 'once'")
	if code != 0 {
		t.Fatalf("nested exec exit = %d; want 0", code)
	}
	if nested != "once" {
		t.Errorf("nested wrapped exec must print the output once with no capture wrap; got %q", nested)
	}
}

// The config-gate inverted: with a scrubbed env (no CALM_ADAPTER_*, no key),
// init-written $CALM_HOME files let a fresh hook/exec resolve config and capture.
func TestInitPairsKeylessEnvironment(t *testing.T) {
	for _, kv := range os.Environ() {
		if k, _, ok := strings.Cut(kv, "="); ok && strings.HasPrefix(k, "CALM_ADAPTER_") {
			t.Setenv(k, "")
		}
	}
	root := t.TempDir()
	installer := capturecli.Deps{
		Cfg: config.Config{Calm: config.CalmConfig{
			URL:               env.serverURL,
			Client:            "default",
			APIKey:            secrets.Secret("[text:" + testMasterKey + "]"),
			SessionTTLMinutes: testDefaultTTLMinutes,
		}},
		Logger: logging.Nop(),
		Client: realCalmClient(t),
		Root:   root,
	}
	if _, errb, code := runCLI(t, installer, "init", "--harness=claude"); code != 0 {
		t.Fatalf("init --harness exit = %d; want 0\nstderr:\n%s", code, errb)
	}

	// Re-resolve config purely from the written files — the runtime env carries
	// no credential.
	cfg, err := config.Load("", root)
	if err != nil {
		t.Fatalf("re-resolve config from $CALM_HOME: %v", err)
	}
	key, err := secrets.Resolve(cfg.Calm.APIKey)
	if err != nil {
		t.Fatalf("resolve written credential: %v", err)
	}
	keyless, err := calm.NewGenapiClient(cfg.Calm.URL, key, nil)
	if err != nil {
		t.Fatalf("client from written config: %v", err)
	}
	runtime := capturecli.Deps{Cfg: cfg, Logger: logging.Nop(), Client: keyless, Root: root}

	marker := "keylessmarker"
	out, _, code := runCLI(t, runtime, "exec", "--session", uniqueSessionID(t), "--", padCommand(marker))
	if code != 0 {
		t.Fatalf("keyless exec exit = %d; want 0", code)
	}
	if strings.Contains(out, obs.DegradedPhrase(obs.DegradedReasonCalmUnreachable)) || strings.Contains(out, obs.DegradedPhrase(obs.DegradedReasonAuthFailed)) {
		t.Fatalf("keyless exec must capture, not degrade:\n%s", out)
	}
	if !strings.Contains(out, `under "calm:v1:`) {
		t.Fatalf("keyless exec must carry the capture's address:\n%s", out)
	}
}
