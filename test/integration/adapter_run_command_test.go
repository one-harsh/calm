// Copyright 2026 The CALM Authors
// SPDX-License-Identifier: Apache-2.0

package integration

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	logging "github.com/one-harsh/context-logging"

	"github.com/one-harsh/calm/internal/adapter/calm"
	"github.com/one-harsh/calm/internal/adapter/mcp"
	"github.com/one-harsh/calm/internal/adapter/obs"
	"github.com/one-harsh/calm/internal/api/genapi"
)

// recordingClient captures the token the adapter mints (re-recording on every
// create, so session replacement is observable) so a test can search the
// adapter's session.
type recordingClient struct {
	calm.Client
	mu    sync.Mutex
	token string
}

func (r *recordingClient) CreateSession(ctx context.Context, client string, ttlMinutes int, idempotencyKey string) (string, error) {
	tok, err := r.Client.CreateSession(ctx, client, ttlMinutes, idempotencyKey)
	if err == nil {
		r.mu.Lock()
		r.token = tok
		r.mu.Unlock()
	}
	return tok, err
}

func (r *recordingClient) sessionToken() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.token
}

type adapterRPCResponse struct {
	ID     json.RawMessage `json:"id"`
	Result json.RawMessage `json:"result"`
	Error  *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

// mcpDriver speaks JSON-RPC to an mcp.Server over in-memory pipes (the stdio substrate).
type mcpDriver struct {
	t      *testing.T
	inW    *io.PipeWriter
	outR   *bufio.Reader
	nextID int
}

func newMCPDriver(t *testing.T, client calm.Client, launchDir string) *mcpDriver {
	t.Helper()
	srv := mcp.NewServer(mcp.Config{
		Calm:              client,
		Logger:            logging.Nop(),
		ServerName:        "calm-adapter",
		ServerVersion:     "test",
		SessionTTLMinutes: testDefaultTTLMinutes,
		LaunchDir:         launchDir,
		// Per-test unique: the suite shares one CALM, so a repeated key would
		// hit the create-dedup window and hand two tests the same session.
		SessionIdempotencyKey: fmt.Sprintf("adapter-%s-%d", t.Name(), time.Now().UnixNano()),
	})
	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- srv.Serve(ctx, inR, outW) }()

	t.Cleanup(func() {
		cancel()
		_ = inW.Close()
		select {
		case <-done:
		case <-time.After(3 * time.Second):
			t.Error("adapter Serve did not exit")
		}
	})
	return &mcpDriver{t: t, inW: inW, outR: bufio.NewReader(outR)}
}

func (d *mcpDriver) call(method string, params any) adapterRPCResponse {
	d.t.Helper()
	d.nextID++
	msg := map[string]any{"jsonrpc": "2.0", "id": d.nextID, "method": method}
	if params != nil {
		msg["params"] = params
	}
	b, err := json.Marshal(msg)
	if err != nil {
		d.t.Fatalf("marshal request: %v", err)
	}
	if _, err := d.inW.Write(append(b, '\n')); err != nil {
		d.t.Fatalf("write request: %v", err)
	}
	line, err := d.outR.ReadBytes('\n')
	if err != nil {
		d.t.Fatalf("read response: %v", err)
	}
	var r adapterRPCResponse
	if err := json.Unmarshal(line, &r); err != nil {
		d.t.Fatalf("decode response: %v", err)
	}
	return r
}

func (d *mcpDriver) runCommand(command string) mcp.ToolResult {
	d.t.Helper()
	r := d.call("tools/call", map[string]any{
		"name":      "calm_run_command",
		"arguments": map[string]any{"command": command},
	})
	if r.Error != nil {
		d.t.Fatalf("tools/call protocol error: %+v", r.Error)
	}
	var res mcp.ToolResult
	if err := json.Unmarshal(r.Result, &res); err != nil {
		d.t.Fatalf("decode tool result: %v", err)
	}
	if len(res.Content) == 0 {
		d.t.Fatalf("tool result had no content: %+v", res)
	}
	return res
}

// newAdapterLoop wires a driver to a real CALM client and initializes a session,
// returning the recording client (for search/read assertions and replacement
// observation) and the token the adapter minted.
func newAdapterLoop(t *testing.T, workspace string) (*recordingClient, string, *mcpDriver) {
	t.Helper()
	inner, err := calm.NewGenapiClient(env.serverURL, testMasterKey, nil)
	if err != nil {
		t.Fatalf("NewGenapiClient: %v", err)
	}
	rc := &recordingClient{Client: inner}
	d := newMCPDriver(t, rc, workspace)

	// Empty clientInfo → adapter creates the session under the seeded default client.
	if r := d.call("initialize", map[string]any{}); r.Error != nil {
		t.Fatalf("initialize: %+v", r.Error)
	}
	token := rc.sessionToken()
	if token == "" {
		t.Fatal("adapter did not create a session on initialize")
	}
	return rc, token, d
}

// parseSearchSource extracts the source label the presentation advertises,
// stripping the fused `@<token>` staleness suffix (per LABELING.md §2) so
// callers that hit CALM directly — e.g., hitCount — see the CALM-facing base.
// parseSearchSourceFused returns the raw suffix-bearing form for tests that
// exercise the fused round-trip through the adapter's calm_search.
func parseSearchSource(t *testing.T, text string) string {
	t.Helper()
	fused := parseSearchSourceFused(t, text)
	if at := strings.LastIndex(fused, "@"); at >= 0 {
		return fused[:at]
	}
	return fused
}

// parseSearchSourceFused reads the fused label from the capture-label line every
// labeled presentation carries — `Captured … under "<fused>".` — so it holds
// whether the output came back as a verbatim whole presentation (compact
// address only) or a summary digest (address plus retrieval command). Only a
// sub-floor inline capture omits a label, and every caller here pushes past the
// floor on purpose.
func parseSearchSourceFused(t *testing.T, text string) string {
	t.Helper()
	const key = `under "`
	idx := strings.LastIndex(text, key)
	if idx < 0 {
		t.Fatalf("presentation carries no capture label:\n%s", text)
	}
	rest := text[idx+len(key):]
	if cut := strings.IndexByte(rest, '"'); cut >= 0 {
		return rest[:cut]
	}
	t.Fatalf("capture label not terminated:\n%s", text)
	return ""
}

func writeWorkspaceFile(t *testing.T, dir, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o600); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
}

// inlinePad pushes a capture past the label-less inline floor so the
// presentation advertises a recall label — for tests whose subject is labeling,
// not presentation. At this size a whole-consumption capture comes back verbatim
// with a compact address; the mutation surfaces still digest.
var inlinePad = strings.Repeat("pad ", 160)

// eventData reads back the first persisted event of eventType through CALM,
// returning its priority and Data. Emission is fire-and-forget, so it polls until
// the event lands (or the deadline) rather than reading once.
func eventData(t *testing.T, token, eventType string) (int, map[string]any) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for {
		resp, err := env.client.ReadEventsWithResponse(
			context.Background(),
			&genapi.ReadEventsParams{XCALMSessionToken: token},
		)
		if err != nil {
			t.Fatalf("ReadEvents: %v", err)
		}
		if resp.StatusCode() != http.StatusOK || resp.JSON200 == nil {
			t.Fatalf("ReadEvents status = %d; body=%s", resp.StatusCode(), string(resp.Body))
		}
		for _, e := range resp.JSON200.Events {
			if e.Type == eventType {
				return e.Priority, e.Data
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("no %q event for the session within the deadline", eventType)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// The headline loop: run through the adapter → output captured → the advertised
// source label retrieves it.
func TestAdapterRunCommand_CaptureIngestSearchLoop(t *testing.T) {
	workspace := t.TempDir()
	const marker = "zphloxmarker"
	writeWorkspaceFile(t, workspace, "note.txt", marker+" lives in this file\n"+inlinePad)

	inner, token, d := newAdapterLoop(t, workspace)

	res := d.runCommand("cat note.txt")
	if res.IsError {
		t.Fatalf("run_command errored: %+v", res)
	}
	source := parseSearchSource(t, res.Content[0].Text)
	if source != "calm:v1:file:read:note.txt" {
		t.Fatalf("advertised source (base) = %q; want calm:v1:file:read:note.txt", source)
	}
	// The LLM-facing recall hint carries the fused staleness suffix per
	// LABELING.md §2 — a 6-char base32 token appended after `@`.
	fused := parseSearchSourceFused(t, res.Content[0].Text)
	if !strings.HasPrefix(fused, source+"@") {
		t.Fatalf("advertised source (fused) = %q; want prefix %q@", fused, source)
	}
	if tail := fused[len(source)+1:]; len(tail) != 6 {
		t.Errorf("fused suffix = %q; want a 6-char token", tail)
	}
	if n := hitCount(t, inner, token, source, marker); n == 0 {
		t.Fatalf("captured output not retrievable via advertised source %q", source)
	}
}

// Inline mode (DESIGN.md §4): a small output comes back as the raw bytes
// verbatim with no recall label in visible text — while still being captured
// into CALM and retrievable via session-wide search.
func TestAdapterRunCommand_SmallOutputInline_StillSearchable(t *testing.T) {
	workspace := t.TempDir()
	const marker = "zphloxinline"
	content := marker + " fits inline\n"
	writeWorkspaceFile(t, workspace, "note.txt", content)

	_, _, d := newAdapterLoop(t, workspace)

	res := d.runCommand("cat note.txt")
	if res.IsError {
		t.Fatalf("run_command errored: %+v", res)
	}
	if got := res.Content[0].Text; got != content {
		t.Fatalf("inline text = %q; want raw output verbatim %q", got, content)
	}

	// Capture is presentation-independent: session-wide search still finds it.
	sr := d.search([]string{marker}, "")
	if sr.IsError {
		t.Fatalf("session-wide search errored: %+v", sr)
	}
	if !strings.Contains(sr.Content[0].Text, marker) {
		t.Fatalf("inline-mode capture not retrievable via session-wide search; got:\n%s", sr.Content[0].Text)
	}
}

// never-worse: with CALM unreachable the adapter still returns the local
// output, prefixed with the canonical calm_unreachable degradation phrasing so
// the agent can branch on the reason.
func TestAdapterRunCommand_CalmDown_UnreachablePhrasingThenRaw(t *testing.T) {
	// Port 1 refuses connections, so session-create fails and the adapter degrades.
	inner, err := calm.NewGenapiClient("http://127.0.0.1:1", "", nil)
	if err != nil {
		t.Fatalf("NewGenapiClient: %v", err)
	}
	d := newMCPDriver(t, inner, t.TempDir())

	if r := d.call("initialize", map[string]any{}); r.Error != nil {
		t.Fatalf("initialize must still succeed when CALM is down: %+v", r.Error)
	}

	res := d.runCommand("echo rawfallback")
	if res.IsError {
		t.Fatalf("CALM-down must not be an error result: %+v", res)
	}
	want := obs.DegradedPhrase(obs.DegradedReasonCalmUnreachable) + "\n\nrawfallback\n"
	if got := res.Content[0].Text; got != want {
		t.Errorf("text = %q; want canonical phrasing then raw: %q", got, want)
	}
}

// coexist: two unrecognized commands each land under their own history source — no clobber.
func TestAdapterRunCommand_CoexistPreservesEachInvocation(t *testing.T) {
	inner, token, d := newAdapterLoop(t, t.TempDir())

	first := parseSearchSource(t, d.runCommand("echo zphloxalpha " + inlinePad).Content[0].Text)
	second := parseSearchSource(t, d.runCommand("echo zphloxbeta " + inlinePad).Content[0].Text)
	if first == second {
		t.Fatalf("coexist invocations collided on one source: %q", first)
	}

	if n := hitCount(t, inner, token, first, "zphloxalpha"); n == 0 {
		t.Errorf("first invocation not retrievable under %q", first)
	}
	if n := hitCount(t, inner, token, second, "zphloxbeta"); n == 0 {
		t.Errorf("second invocation not retrievable under %q", second)
	}
	if n := hitCount(t, inner, token, first, "zphloxbeta"); n != 0 {
		t.Errorf("first invocation was clobbered by the second (%d stray hits)", n)
	}
}

// idempotent-indexing: re-reading a changed file replaces the prior snapshot under one label.
func TestAdapterRunCommand_ReplaceDedupsOnRerun(t *testing.T) {
	workspace := t.TempDir()
	writeWorkspaceFile(t, workspace, "note.txt", "zphloxfirst here\n"+inlinePad)

	inner, token, d := newAdapterLoop(t, workspace)

	first := parseSearchSource(t, d.runCommand("cat note.txt").Content[0].Text)
	writeWorkspaceFile(t, workspace, "note.txt", "zphloxsecond here\n"+inlinePad)
	second := parseSearchSource(t, d.runCommand("cat note.txt").Content[0].Text)

	if first != second {
		t.Fatalf("replace mode must reuse one source: %q vs %q", first, second)
	}
	if n := hitCount(t, inner, token, second, "zphloxsecond"); n == 0 {
		t.Errorf("latest content not retrievable after rerun")
	}
	if n := hitCount(t, inner, token, second, "zphloxfirst"); n != 0 {
		t.Errorf("stale content survived replace (%d hits)", n)
	}
}

// dual: git keeps both a stable latest source and a per-invocation history source,
// and the event cross-links exactly the sources that persisted.
func TestAdapterRunCommand_DualPersistsHistoryAndFreshLatest(t *testing.T) {
	dir := gitRepo(t)
	// Extra untracked files push `git status` output past the inline
	// threshold so the recall label is advertised.
	for i := range 8 {
		writeWorkspaceFile(t, dir, fmt.Sprintf("pad-%02d-%s.txt", i, strings.Repeat("p", 60)), "pad\n")
	}
	inner, token, d := newAdapterLoop(t, dir)

	res := d.runCommand("git status")
	if res.IsError {
		t.Fatalf("run_command errored: %+v", res)
	}
	latest := parseSearchSource(t, res.Content[0].Text)
	if latest != "calm:v1:vcs:git:status" {
		t.Fatalf("dual latest source = %q; want calm:v1:vcs:git:status", latest)
	}
	history := "calm:v1:vcs:git:status#1"

	if n := hitCount(t, inner, token, latest, "zphloxtracked.txt"); n == 0 {
		t.Errorf("git status output not retrievable via latest source %q", latest)
	}
	if n := hitCount(t, inner, token, history, "zphloxtracked.txt"); n == 0 {
		t.Errorf("history source %q not retrievable", history)
	}

	invP, inv := eventData(t, token, "tool_invocation")
	if invP != 3 {
		t.Errorf("tool_invocation priority = %d; want 3", invP)
	}
	if inv["latest_source"] != latest {
		t.Errorf("event latest_source = %v; want %q", inv["latest_source"], latest)
	}
	if inv["history_source"] != history {
		t.Errorf("event history_source = %v; want %q", inv["history_source"], history)
	}
	// A git command also emits a git_operation event (eventData fails if absent).
	gitP, gitOp := eventData(t, token, "git_operation")
	if gitP != 2 {
		t.Errorf("git_operation priority = %d; want 2", gitP)
	}
	if gitOp["subcommand"] != "status" {
		t.Errorf("git_operation subcommand = %v; want status", gitOp["subcommand"])
	}
}

// event emission: a run persists a tool_invocation event cross-linked to the ingested source.
func TestAdapterRunCommand_EmitsRetrievableEvents(t *testing.T) {
	_, token, d := newAdapterLoop(t, t.TempDir())

	if res := d.runCommand("echo zphloxevent"); res.IsError {
		t.Fatalf("run_command errored: %+v", res)
	}

	p, inv := eventData(t, token, "tool_invocation")
	if p != 3 {
		t.Errorf("tool_invocation priority = %d; want 3", p)
	}
	if inv["tool_name"] != "calm_run_command" {
		t.Errorf("event tool_name = %v; want calm_run_command", inv["tool_name"])
	}
	if inv["history_source"] != "calm:v1:shell:echo#1" {
		t.Errorf("event history_source = %v; want calm:v1:shell:echo#1", inv["history_source"])
	}
}

// a failed command still completes (result captured, not a tool error) and emits
// error_observed at P2.
func TestAdapterRunCommand_ErrorExitEmitsErrorEvent(t *testing.T) {
	_, token, d := newAdapterLoop(t, t.TempDir())

	if res := d.runCommand("exit 7"); res.IsError {
		t.Fatalf("non-zero exit must be a captured result, not a tool error: %+v", res)
	}

	p, ed := eventData(t, token, "error_observed")
	if p != 2 {
		t.Errorf("error_observed priority = %d; want 2", p)
	}
	if msg, _ := ed["message"].(string); !strings.Contains(msg, "7") {
		t.Errorf("error_observed message = %q; want it to mention exit code 7", msg)
	}
}

// gitRepo creates a throwaway repo with one untracked file whose name is a searchable marker.
func gitRepo(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	dir := t.TempDir()
	writeWorkspaceFile(t, dir, "zphloxtracked.txt", "tracked content\n")

	cmd := exec.Command("git", "init")
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git init: %v\n%s", err, out)
	}
	return dir
}
