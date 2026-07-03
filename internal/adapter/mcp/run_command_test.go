// Copyright 2026 The CALM Authors
// SPDX-License-Identifier: Apache-2.0

package mcp_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/mock"

	"github.com/one-harsh/calm/internal/adapter/calm"
	"github.com/one-harsh/calm/internal/adapter/mcp"
	"github.com/one-harsh/calm/internal/adapter/obs"
)

// writeEventsSignal sets a WriteEvents expectation that closes the returned channel
// when invoked, so the fire-and-forget emission can be awaited deterministically.
func writeEventsSignal(m *calm.MockClient, err error) <-chan struct{} {
	done := make(chan struct{})
	m.EXPECT().WriteEvents(mock.Anything, mock.Anything, mock.Anything).
		RunAndReturn(func(context.Context, string, []calm.EventInput) error {
			close(done)
			return err
		}).Once()
	return done
}

func awaitSignal(t *testing.T, done <-chan struct{}) {
	t.Helper()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("WriteEvents was not invoked")
	}
}

func initSession(t *testing.T, h *harness, client string) {
	t.Helper()
	h.send(req(1, "initialize", map[string]any{"clientInfo": map[string]any{"name": client}}))
	if r := h.recv(); r.Error != nil {
		t.Fatalf("initialize error: %+v", r.Error)
	}
}

func callRunCommand(t *testing.T, h *harness, id int, args map[string]any) mcp.ToolResult {
	t.Helper()
	h.send(req(id, "tools/call", map[string]any{"name": "calm_run_command", "arguments": args}))
	r := h.recv()
	if r.Error != nil {
		t.Fatalf("tools/call protocol error: %+v", r.Error)
	}
	var res mcp.ToolResult
	if err := json.Unmarshal(r.Result, &res); err != nil {
		t.Fatalf("decode tool result: %v", err)
	}
	return res
}

func resultText(t *testing.T, res mcp.ToolResult) string {
	t.Helper()
	if len(res.Content) == 0 {
		t.Fatalf("tool result had no content: %+v", res)
	}
	return res.Content[0].Text
}

func TestRunCommand_IngestsAndReturnsCompactRep(t *testing.T) {
	m := calm.NewMockClient(t)
	m.EXPECT().RegisterClient(mock.Anything, "claude-code").Return(true, nil).Once()
	m.EXPECT().CreateSession(mock.Anything, "claude-code", 60, mock.Anything).Return("tok-1", nil).Once()
	m.EXPECT().DeleteSession(mock.Anything, "tok-1").Return(nil).Once()
	m.EXPECT().Ingest(mock.Anything, "tok-1", mock.MatchedBy(func(in calm.IngestInput) bool {
		return in.Source == "calm:v1:shell:echo#1" && in.Content == "hello\n"
	})).Return(calm.IngestSummary{
		Source:          "calm:v1:shell:echo#1",
		SectionsIndexed: 1,
		SectionsTotal:   1,
		Sections:        []calm.SectionPreview{{Title: "output", Preview: "hello"}},
	}, nil).Once()
	eventsDone := writeEventsSignal(m, nil)

	h := newHarness(t, m)
	initSession(t, h, "claude-code")

	res := callRunCommand(t, h, 2, map[string]any{"command": "echo hello"})
	if res.IsError {
		t.Fatalf("unexpected isError: %+v", res)
	}
	text := resultText(t, res)
	if !strings.Contains(text, "calm:v1:shell:echo#1") {
		t.Errorf("compact rep missing source label; got:\n%s", text)
	}
	if !strings.Contains(text, "calm_search source=calm:v1:shell:echo#1") {
		t.Errorf("compact rep missing search handle; got:\n%s", text)
	}
	awaitSignal(t, eventsDone)
}

func TestRunCommand_DualWritesHistoryThenLatest(t *testing.T) {
	m := calm.NewMockClient(t)
	m.EXPECT().RegisterClient(mock.Anything, "claude-code").Return(true, nil).Once()
	m.EXPECT().CreateSession(mock.Anything, "claude-code", 60, mock.Anything).Return("tok-1", nil).Once()
	m.EXPECT().DeleteSession(mock.Anything, "tok-1").Return(nil).Once()
	eventsDone := writeEventsSignal(m, nil)

	var mu sync.Mutex
	var order []string
	m.EXPECT().Ingest(mock.Anything, "tok-1", mock.Anything).RunAndReturn(
		func(_ context.Context, _ string, in calm.IngestInput) (calm.IngestSummary, error) {
			mu.Lock()
			order = append(order, in.Source)
			mu.Unlock()
			return calm.IngestSummary{Source: in.Source, SectionsIndexed: 1, SectionsTotal: 1}, nil
		},
	).Times(2)

	h := newHarness(t, m)
	initSession(t, h, "claude-code")

	res := callRunCommand(t, h, 2, map[string]any{"command": "git status"})
	if res.IsError {
		t.Fatalf("unexpected isError: %+v", res)
	}

	mu.Lock()
	defer mu.Unlock()
	want := []string{"calm:v1:vcs:git:status#1", "calm:v1:vcs:git:status"}
	if len(order) != 2 || order[0] != want[0] || order[1] != want[1] {
		t.Fatalf("ingest order = %v; want history-then-latest %v", order, want)
	}
	awaitSignal(t, eventsDone)
}

func TestRunCommand_IngestFailure_CapturePhrasingThenRaw(t *testing.T) {
	m := calm.NewMockClient(t)
	m.EXPECT().RegisterClient(mock.Anything, "claude-code").Return(true, nil).Once()
	m.EXPECT().CreateSession(mock.Anything, "claude-code", 60, mock.Anything).Return("tok-1", nil).Once()
	m.EXPECT().DeleteSession(mock.Anything, "tok-1").Return(nil).Once()
	m.EXPECT().Ingest(mock.Anything, "tok-1", mock.Anything).
		Return(calm.IngestSummary{}, errors.New("boom")).Once()
	// tool_invocation event is still emitted (best-effort), just without source links.
	eventsDone := writeEventsSignal(m, nil)

	h := newHarness(t, m)
	initSession(t, h, "claude-code")

	res := callRunCommand(t, h, 2, map[string]any{"command": "echo hello"})
	if res.IsError {
		t.Fatalf("ingest failure must not be an error result: %+v", res)
	}
	want := obs.DegradedPhrase(obs.DegradedReasonCaptureFailed) + "\n\nhello\n"
	if got := resultText(t, res); got != want {
		t.Errorf("text = %q; want capture_failed phrasing prefix then raw: %q", got, want)
	}
	awaitSignal(t, eventsDone)
}

// Dual-mode with both ingests failing — the canonical capture_failed phrasing
// prepends the raw output and IsError stays false (never-worse: local action
// succeeded, only capture is degraded).
func TestRunCommand_DualMode_BothFail_CapturePhrasing(t *testing.T) {
	m := calm.NewMockClient(t)
	m.EXPECT().RegisterClient(mock.Anything, "claude-code").Return(true, nil).Once()
	m.EXPECT().CreateSession(mock.Anything, "claude-code", 60, mock.Anything).Return("tok-1", nil).Once()
	m.EXPECT().DeleteSession(mock.Anything, "tok-1").Return(nil).Once()
	m.EXPECT().Ingest(mock.Anything, "tok-1", mock.Anything).
		Return(calm.IngestSummary{}, errors.New("boom")).Times(2)
	eventsDone := writeEventsSignal(m, nil)

	h := newHarness(t, m)
	initSession(t, h, "claude-code")

	res := callRunCommand(t, h, 2, map[string]any{"command": "git status"})
	if res.IsError {
		t.Fatalf("dual-mode both-fail must not be an error result: %+v", res)
	}
	got := resultText(t, res)
	if !strings.HasPrefix(got, obs.DegradedPhrase(obs.DegradedReasonCaptureFailed)) {
		t.Errorf("text = %q; want prefix %q", got, obs.DegradedPhrase(obs.DegradedReasonCaptureFailed))
	}
	awaitSignal(t, eventsDone)
}

// Dual-mode with one ingest succeeding and one failing — canonical
// capture_partial phrasing prepends the compact rep of the persisted source.
func TestRunCommand_DualMode_PartialFail_CapturePartialPhrasing(t *testing.T) {
	m := calm.NewMockClient(t)
	m.EXPECT().RegisterClient(mock.Anything, "claude-code").Return(true, nil).Once()
	m.EXPECT().CreateSession(mock.Anything, "claude-code", 60, mock.Anything).Return("tok-1", nil).Once()
	m.EXPECT().DeleteSession(mock.Anything, "tok-1").Return(nil).Once()
	// History (first per dual ordering) succeeds; latest (second) fails.
	var calls int
	var mu sync.Mutex
	m.EXPECT().Ingest(mock.Anything, "tok-1", mock.Anything).RunAndReturn(
		func(_ context.Context, _ string, in calm.IngestInput) (calm.IngestSummary, error) {
			mu.Lock()
			calls++
			n := calls
			mu.Unlock()
			if n == 1 {
				return calm.IngestSummary{Source: in.Source, SectionsIndexed: 1, SectionsTotal: 1}, nil
			}
			return calm.IngestSummary{}, errors.New("boom")
		},
	).Times(2)
	eventsDone := writeEventsSignal(m, nil)

	h := newHarness(t, m)
	initSession(t, h, "claude-code")

	res := callRunCommand(t, h, 2, map[string]any{"command": "git status"})
	if res.IsError {
		t.Fatalf("dual-mode partial-fail must not be an error result: %+v", res)
	}
	got := resultText(t, res)
	if !strings.HasPrefix(got, obs.DegradedPhrase(obs.DegradedReasonCapturePartial)) {
		t.Errorf("text = %q; want prefix %q", got, obs.DegradedPhrase(obs.DegradedReasonCapturePartial))
	}
	// The compact rep of the persisted (history) source follows the phrasing.
	if !strings.Contains(got, "Captured 1/1 sections under") {
		t.Errorf("text missing compact rep of persisted source; got %q", got)
	}
	awaitSignal(t, eventsDone)
}

// A command writing to both stdout and stderr ingests both into CALM, with
// stream markers distinguishing them. No allowlist — any process that wrote
// stderr has its diagnostics preserved through to later scoped-search.
func TestRunCommand_StderrPresent_IngestedAlongsideStdout(t *testing.T) {
	m := calm.NewMockClient(t)
	m.EXPECT().RegisterClient(mock.Anything, "claude-code").Return(true, nil).Once()
	m.EXPECT().CreateSession(mock.Anything, "claude-code", 60, mock.Anything).Return("tok-1", nil).Once()
	m.EXPECT().DeleteSession(mock.Anything, "tok-1").Return(nil).Once()

	var ingested string
	m.EXPECT().Ingest(mock.Anything, "tok-1", mock.MatchedBy(func(in calm.IngestInput) bool {
		ingested = in.Content
		return true
	})).Return(calm.IngestSummary{Source: "calm:v1:shell:sh#1", SectionsIndexed: 1, SectionsTotal: 1}, nil).Once()
	eventsDone := writeEventsSignal(m, nil)

	h := newHarness(t, m)
	initSession(t, h, "claude-code")

	res := callRunCommand(t, h, 2, map[string]any{"command": "sh -c 'echo stdoutline; echo stderrline >&2'"})
	if res.IsError {
		t.Fatalf("unexpected isError: %+v", res)
	}
	for _, want := range []string{"[stdout]", "stdoutline", "[stderr]", "stderrline"} {
		if !strings.Contains(ingested, want) {
			t.Errorf("ingested content missing %q; got:\n%s", want, ingested)
		}
	}
	awaitSignal(t, eventsDone)
}

// On the never-worse raw-fallback path, the visible result must carry both
// stdout and stderr from the captured command — losing stderr here would
// mean the agent has no diagnostic from a failing build/test/compile.
func TestRunCommand_StderrPresent_CalmDownVisibleIncludesBoth(t *testing.T) {
	m := calm.NewMockClient(t)
	h := newHarness(t, m)

	res := callRunCommand(t, h, 1, map[string]any{"command": "sh -c 'echo stdoutline; echo stderrline >&2'"})
	if res.IsError {
		t.Fatalf("CALM-down must not be an error result: %+v", res)
	}
	text := resultText(t, res)
	for _, want := range []string{"[stdout]", "stdoutline", "[stderr]", "stderrline"} {
		if !strings.Contains(text, want) {
			t.Errorf("visible text missing %q; got:\n%s", want, text)
		}
	}
}

func TestRunCommand_CalmDown_UnreachablePhrasingThenRaw(t *testing.T) {
	// No initialize → no session token → degraded mode. Zero mock expectations,
	// so any CALM call would fail the test.
	m := calm.NewMockClient(t)
	h := newHarness(t, m)

	res := callRunCommand(t, h, 1, map[string]any{"command": "echo hello"})
	if res.IsError {
		t.Fatalf("CALM-down must not be an error result: %+v", res)
	}
	want := obs.DegradedPhrase(obs.DegradedReasonCalmUnreachable) + "\n\nhello\n"
	if got := resultText(t, res); got != want {
		t.Errorf("text = %q; want calm_unreachable phrasing prefix then raw: %q", got, want)
	}
}

func TestRunCommand_BlankCommandIsError(t *testing.T) {
	m := calm.NewMockClient(t)
	h := newHarness(t, m)

	res := callRunCommand(t, h, 1, map[string]any{"command": "   "})
	if !res.IsError {
		t.Fatalf("blank command must be an error result: %+v", res)
	}
	if got := resultText(t, res); !strings.Contains(got, "command is required") {
		t.Errorf("error text = %q; want 'command is required'", got)
	}
}

func TestRunCommand_EventFailureStillReturnsCompactRep(t *testing.T) {
	m := calm.NewMockClient(t)
	m.EXPECT().RegisterClient(mock.Anything, "claude-code").Return(true, nil).Once()
	m.EXPECT().CreateSession(mock.Anything, "claude-code", 60, mock.Anything).Return("tok-1", nil).Once()
	m.EXPECT().DeleteSession(mock.Anything, "tok-1").Return(nil).Once()
	m.EXPECT().Ingest(mock.Anything, "tok-1", mock.Anything).Return(calm.IngestSummary{
		Source:          "calm:v1:shell:echo#1",
		SectionsIndexed: 1,
		SectionsTotal:   1,
	}, nil).Once()
	eventsDone := writeEventsSignal(m, errors.New("events path unavailable"))

	h := newHarness(t, m)
	initSession(t, h, "claude-code")

	res := callRunCommand(t, h, 2, map[string]any{"command": "echo hello"})
	if res.IsError {
		t.Fatalf("event failure must not break the response: %+v", res)
	}
	if got := resultText(t, res); !strings.Contains(got, "calm:v1:shell:echo#1") {
		t.Errorf("compact rep missing source label after event failure; got:\n%s", got)
	}
	awaitSignal(t, eventsDone)
}

func TestRunCommand_InvalidArgumentsIsError(t *testing.T) {
	m := calm.NewMockClient(t)
	h := newHarness(t, m)

	// arguments as a JSON string (not an object) fails to unmarshal into the schema.
	h.send(req(1, "tools/call", map[string]any{"name": "calm_run_command", "arguments": "not-an-object"}))
	r := h.recv()
	if r.Error != nil {
		t.Fatalf("want isError result, got protocol error: %+v", r.Error)
	}
	var res mcp.ToolResult
	if err := json.Unmarshal(r.Result, &res); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !res.IsError || !strings.Contains(resultText(t, res), "invalid arguments") {
		t.Fatalf("want invalid-arguments error result; got %+v", res)
	}
}

// never-worse: a stalled /v1/events must not hold the tool response. With fire-and-forget
// emission the compact rep returns even while WriteEvents is blocked.
func TestRunCommand_BlockedEventWriteDoesNotHoldResponse(t *testing.T) {
	m := calm.NewMockClient(t)
	m.EXPECT().RegisterClient(mock.Anything, "claude-code").Return(true, nil).Once()
	m.EXPECT().CreateSession(mock.Anything, "claude-code", 60, mock.Anything).Return("tok-1", nil).Once()
	m.EXPECT().DeleteSession(mock.Anything, "tok-1").Return(nil).Once()
	m.EXPECT().Ingest(mock.Anything, "tok-1", mock.Anything).Return(calm.IngestSummary{
		Source: "calm:v1:shell:echo#1", SectionsIndexed: 1, SectionsTotal: 1,
	}, nil).Once()

	started := make(chan struct{})
	release := make(chan struct{})
	m.EXPECT().WriteEvents(mock.Anything, "tok-1", mock.Anything).
		RunAndReturn(func(ctx context.Context, _ string, _ []calm.EventInput) error {
			close(started)
			select {
			case <-release:
			case <-ctx.Done(): // emitEvents' own timeout
			}
			return nil
		}).Once()

	h := newHarness(t, m)
	t.Cleanup(func() { close(release) }) // unblock the goroutine before teardown
	initSession(t, h, "claude-code")

	h.send(req(2, "tools/call", map[string]any{
		"name": "calm_run_command", "arguments": map[string]any{"command": "echo hello"},
	}))

	// Read the response off the wire on a deadline. If emission were synchronous, the
	// blocked WriteEvents would hold this past the deadline.
	type readResult struct {
		line []byte
		err  error
	}
	got := make(chan readResult, 1)
	go func() {
		line, err := h.outR.ReadBytes('\n')
		got <- readResult{line, err}
	}()

	select {
	case rr := <-got:
		if rr.err != nil {
			t.Fatalf("recv: %v", rr.err)
		}
		var resp rpcResp
		if err := json.Unmarshal(rr.line, &resp); err != nil {
			t.Fatalf("decode response: %v", err)
		}
		var res mcp.ToolResult
		if err := json.Unmarshal(resp.Result, &res); err != nil {
			t.Fatalf("decode result: %v", err)
		}
		if res.IsError || !strings.Contains(resultText(t, res), "calm:v1:shell:echo#1") {
			t.Fatalf("want compact rep, got %+v", res)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("tool response was held hostage by a blocked WriteEvents (never-worse)")
	}

	awaitSignal(t, started) // emission ran, so the WriteEvents expectation is satisfied
}

// never-worse: a mid-ingest panic still returns the raw output via the handler recover.
// A panic mid-handler is recovered and surfaced as canonical capture_failed
// degradation — never-worse: raw output is preserved, prefixed with the
// phrasing so the LLM can branch on the reason.
func TestRunCommand_IngestPanic_CapturePhrasingThenRaw(t *testing.T) {
	m := calm.NewMockClient(t)
	m.EXPECT().RegisterClient(mock.Anything, "claude-code").Return(true, nil).Once()
	m.EXPECT().CreateSession(mock.Anything, "claude-code", 60, mock.Anything).Return("tok-1", nil).Once()
	m.EXPECT().DeleteSession(mock.Anything, "tok-1").Return(nil).Once()
	m.EXPECT().Ingest(mock.Anything, "tok-1", mock.Anything).
		RunAndReturn(func(context.Context, string, calm.IngestInput) (calm.IngestSummary, error) {
			panic("ingest blew up")
		}).Once()

	h := newHarness(t, m)
	initSession(t, h, "claude-code")

	res := callRunCommand(t, h, 2, map[string]any{"command": "echo hello"})
	if res.IsError {
		t.Fatalf("panic must fall back to a non-error raw result: %+v", res)
	}
	want := obs.DegradedPhrase(obs.DegradedReasonCaptureFailed) + "\n\nhello\n"
	if got := resultText(t, res); got != want {
		t.Errorf("text = %q; want capture_failed phrasing then raw: %q", got, want)
	}
}

// Guards the default-raw mechanism the recover relies on: a panic after a partial
// ingest (rep set) but before the response is finalized must still yield raw,
// prefixed with capture_failed phrasing — not the compact rep that was about to
// be built. Drop the `res = TextResult(raw, false)` default and this fails.
func TestRunCommand_PanicBeforeSummaryBuilt_CapturePhrasingThenRaw(t *testing.T) {
	m := calm.NewMockClient(t)
	m.EXPECT().RegisterClient(mock.Anything, "claude-code").Return(true, nil).Once()
	m.EXPECT().CreateSession(mock.Anything, "claude-code", 60, mock.Anything).Return("tok-1", nil).Once()
	m.EXPECT().DeleteSession(mock.Anything, "tok-1").Return(nil).Once()
	// Dual mode: history ingest succeeds (sets rep), then latest ingest panics —
	// before formatCompact runs, so the default raw must survive the recover.
	m.EXPECT().Ingest(mock.Anything, "tok-1", mock.MatchedBy(func(in calm.IngestInput) bool {
		return in.Source == "calm:v1:vcs:git:status#1"
	})).Return(calm.IngestSummary{Source: "calm:v1:vcs:git:status#1", SectionsIndexed: 1, SectionsTotal: 1}, nil).Once()
	m.EXPECT().Ingest(mock.Anything, "tok-1", mock.MatchedBy(func(in calm.IngestInput) bool {
		return in.Source == "calm:v1:vcs:git:status"
	})).RunAndReturn(func(context.Context, string, calm.IngestInput) (calm.IngestSummary, error) {
		panic("latest ingest blew up")
	}).Once()

	h := newHarness(t, m)
	initSession(t, h, "claude-code")

	res := callRunCommand(t, h, 2, map[string]any{"command": "git status"})
	if res.IsError {
		t.Fatalf("a pre-summary panic must fall back to phrased raw, not an error: %+v", res)
	}
	got := resultText(t, res)
	if !strings.HasPrefix(got, obs.DegradedPhrase(obs.DegradedReasonCaptureFailed)) {
		t.Errorf("text missing capture_failed phrasing prefix; got:\n%s", got)
	}
	if strings.Contains(got, "calm_search source=") {
		t.Errorf("expected raw fallback (compact rep was never built), got rep:\n%s", got)
	}
}

// A 404 on ingest triggers AD03 recovery: one replacement create with a fresh
// idempotency key, registry epoch reset, and the ORIGINAL call surfaces the
// session_lost phrasing with raw output (never-worse) — no transparent retry.
// The dead token's remaining writes and event emission are skipped; the next
// call captures cleanly under the replacement session.
func TestRunCommand_SessionLost_RecreatesAndSignalsSessionLost(t *testing.T) {
	m := calm.NewMockClient(t)
	m.EXPECT().RegisterClient(mock.Anything, "claude-code").Return(true, nil).Once()

	var mu sync.Mutex
	var keys []string
	m.EXPECT().CreateSession(mock.Anything, "claude-code", 60, mock.Anything).RunAndReturn(
		func(_ context.Context, _ string, _ int, key string) (string, error) {
			mu.Lock()
			keys = append(keys, key)
			n := len(keys)
			mu.Unlock()
			if n == 1 {
				return "tok-1", nil
			}
			return "tok-2", nil
		},
	).Times(2)
	m.EXPECT().Ingest(mock.Anything, "tok-1", mock.Anything).
		Return(calm.IngestSummary{}, &calm.StatusError{Op: "ingest", Code: 404, Status: "404 Not Found"}).Once()
	m.EXPECT().Ingest(mock.Anything, "tok-2", mock.Anything).RunAndReturn(
		func(_ context.Context, _ string, in calm.IngestInput) (calm.IngestSummary, error) {
			return calm.IngestSummary{Source: in.Source, SectionsIndexed: 1, SectionsTotal: 1}, nil
		},
	).Once()
	m.EXPECT().DeleteSession(mock.Anything, "tok-2").Return(nil).Once()
	eventsDone := writeEventsSignal(m, nil) // second command only; the dead-token call skips events

	h := newHarness(t, m)
	initSession(t, h, "claude-code")

	res := callRunCommand(t, h, 2, map[string]any{"command": "echo hello"})
	if res.IsError {
		t.Fatalf("session loss on an action tool must not be an error result: %+v", res)
	}
	want := obs.DegradedPhrase(obs.DegradedReasonSessionLost) + "\n\nhello\n"
	if got := resultText(t, res); got != want {
		t.Errorf("text = %q; want session_lost phrasing then raw: %q", got, want)
	}

	res = callRunCommand(t, h, 3, map[string]any{"command": "echo hello"})
	if res.IsError {
		t.Fatalf("post-recovery capture errored: %+v", res)
	}
	if got := resultText(t, res); !strings.Contains(got, "calm_search source=") {
		t.Errorf("post-recovery call did not capture cleanly; got:\n%s", got)
	}
	awaitSignal(t, eventsDone)

	mu.Lock()
	defer mu.Unlock()
	if keys[0] != "idem-base" {
		t.Errorf("initialize key = %q; want the base key verbatim", keys[0])
	}
	if keys[1] == keys[0] {
		t.Errorf("recovery reused the initialize idempotency key %q — CALM's dedup window would replay the dead session", keys[1])
	}
}

// Recovery resets the token registry: a fused label minted before the loss
// rejects locally as session_lost — it never reaches CALM to resolve against
// the replacement session's content.
func TestRunCommand_SessionLost_RegistryReset(t *testing.T) {
	m := calm.NewMockClient(t)
	m.EXPECT().RegisterClient(mock.Anything, "claude-code").Return(true, nil).Once()
	m.EXPECT().CreateSession(mock.Anything, "claude-code", 60, mock.Anything).Return("tok-1", nil).Once()
	m.EXPECT().Ingest(mock.Anything, "tok-1", mock.Anything).RunAndReturn(
		func(_ context.Context, _ string, in calm.IngestInput) (calm.IngestSummary, error) {
			return calm.IngestSummary{Source: in.Source, SectionsIndexed: 1, SectionsTotal: 1}, nil
		},
	).Once()
	eventsDone := writeEventsSignal(m, nil)

	h := newHarness(t, m)
	initSession(t, h, "claude-code")

	res := callRunCommand(t, h, 2, map[string]any{"command": "echo hello"})
	fused := extractRecallLabel(t, resultText(t, res))
	awaitSignal(t, eventsDone)

	m.EXPECT().Ingest(mock.Anything, "tok-1", mock.Anything).
		Return(calm.IngestSummary{}, &calm.StatusError{Op: "ingest", Code: 404, Status: "404 Not Found"}).Once()
	m.EXPECT().CreateSession(mock.Anything, "claude-code", 60, mock.Anything).Return("tok-2", nil).Once()
	m.EXPECT().DeleteSession(mock.Anything, "tok-2").Return(nil).Once()
	if res = callRunCommand(t, h, 3, map[string]any{"command": "echo hello"}); res.IsError {
		t.Fatalf("session-loss call must not be an error result: %+v", res)
	}

	// No Search expectation: the stale fused label must resolve locally.
	res = callSearch(t, h, 4, map[string]any{"queries": []string{"hello"}, "source": fused})
	if !res.IsError {
		t.Fatalf("pre-loss fused label must reject after recovery: %+v", res)
	}
	if got := resultText(t, res); got != obs.DegradedPhrase(obs.DegradedReasonSessionLost) {
		t.Errorf("text = %q; want session_lost phrasing", got)
	}
}

// A recovery create rejected with 4xx latches auth_failed for the process:
// the original call phrases auth_failed over raw output, and every subsequent
// call — action or retrieval — short-circuits without CALM traffic.
func TestRunCommand_SessionLost_CreateRejected_AuthFailedSticky(t *testing.T) {
	m := calm.NewMockClient(t)
	m.EXPECT().RegisterClient(mock.Anything, "claude-code").Return(true, nil).Once()

	var mu sync.Mutex
	var creates int
	m.EXPECT().CreateSession(mock.Anything, "claude-code", 60, mock.Anything).RunAndReturn(
		func(context.Context, string, int, string) (string, error) {
			mu.Lock()
			creates++
			n := creates
			mu.Unlock()
			if n == 1 {
				return "tok-1", nil
			}
			return "", &calm.StatusError{Op: "create session", Code: 401, Status: "401 Unauthorized"}
		},
	).Times(2)
	m.EXPECT().Ingest(mock.Anything, "tok-1", mock.Anything).
		Return(calm.IngestSummary{}, &calm.StatusError{Op: "ingest", Code: 404, Status: "404 Not Found"}).Once()
	// No DeleteSession: the latch clears the session, so shutdown has nothing to delete.

	h := newHarness(t, m)
	initSession(t, h, "claude-code")

	res := callRunCommand(t, h, 2, map[string]any{"command": "echo hello"})
	if res.IsError {
		t.Fatalf("auth failure on an action tool must not be an error result: %+v", res)
	}
	want := obs.DegradedPhrase(obs.DegradedReasonAuthFailed) + "\n\nhello\n"
	if got := resultText(t, res); got != want {
		t.Errorf("text = %q; want auth_failed phrasing then raw: %q", got, want)
	}

	// Sticky: zero further mock expectations — any CALM traffic fails the test.
	res = callRunCommand(t, h, 3, map[string]any{"command": "echo hello"})
	if got := resultText(t, res); got != want {
		t.Errorf("second call text = %q; want short-circuited auth_failed: %q", got, want)
	}
	res = callSearch(t, h, 4, map[string]any{"queries": []string{"hello"}})
	if !res.IsError {
		t.Fatalf("auth-failed search must be an error result: %+v", res)
	}
	if got := resultText(t, res); got != obs.DegradedPhrase(obs.DegradedReasonAuthFailed) {
		t.Errorf("search text = %q; want auth_failed phrasing", got)
	}
}

// A transient recovery-create failure (network error) is calm_unreachable, not
// auth_failed: no latch, the session stays dead, and the next 404 re-attempts
// recovery — which then succeeds.
func TestRunCommand_SessionLost_CreateTransient_CalmUnreachableThenRetries(t *testing.T) {
	m := calm.NewMockClient(t)
	m.EXPECT().RegisterClient(mock.Anything, "claude-code").Return(true, nil).Once()

	var mu sync.Mutex
	var creates int
	m.EXPECT().CreateSession(mock.Anything, "claude-code", 60, mock.Anything).RunAndReturn(
		func(context.Context, string, int, string) (string, error) {
			mu.Lock()
			creates++
			n := creates
			mu.Unlock()
			switch n {
			case 1:
				return "tok-1", nil
			case 2:
				return "", errors.New("dial tcp: connection refused")
			default:
				return "tok-2", nil
			}
		},
	).Times(3)
	m.EXPECT().Ingest(mock.Anything, "tok-1", mock.Anything).
		Return(calm.IngestSummary{}, &calm.StatusError{Op: "ingest", Code: 404, Status: "404 Not Found"}).Times(2)
	m.EXPECT().DeleteSession(mock.Anything, "tok-2").Return(nil).Once()

	h := newHarness(t, m)
	initSession(t, h, "claude-code")

	res := callRunCommand(t, h, 2, map[string]any{"command": "echo hello"})
	got := resultText(t, res)
	if !strings.HasPrefix(got, obs.DegradedPhrase(obs.DegradedReasonCalmUnreachable)) {
		t.Errorf("text = %q; want calm_unreachable phrasing prefix", got)
	}
	if !strings.Contains(got, "[stderr]\ndial tcp: connection refused") {
		t.Errorf("text = %q; want the transient create error as [stderr] detail", got)
	}

	res = callRunCommand(t, h, 3, map[string]any{"command": "echo hello"})
	want := obs.DegradedPhrase(obs.DegradedReasonSessionLost) + "\n\nhello\n"
	if got := resultText(t, res); got != want {
		t.Errorf("retry text = %q; want session_lost after successful re-recovery: %q", got, want)
	}
}

// A 5xx on ingest is NOT a session-loss trigger: classification stays
// capture_failed and no replacement create happens (the single CreateSession
// expectation is the initialize one; strict mocks fail on any second create).
func TestRunCommand_Ingest5xx_NoReplacement(t *testing.T) {
	m := calm.NewMockClient(t)
	m.EXPECT().RegisterClient(mock.Anything, "claude-code").Return(true, nil).Once()
	m.EXPECT().CreateSession(mock.Anything, "claude-code", 60, mock.Anything).Return("tok-1", nil).Once()
	m.EXPECT().DeleteSession(mock.Anything, "tok-1").Return(nil).Once()
	m.EXPECT().Ingest(mock.Anything, "tok-1", mock.Anything).
		Return(calm.IngestSummary{}, &calm.StatusError{Op: "ingest", Code: 500, Status: "500 Internal Server Error"}).Once()
	eventsDone := writeEventsSignal(m, nil)

	h := newHarness(t, m)
	initSession(t, h, "claude-code")

	res := callRunCommand(t, h, 2, map[string]any{"command": "echo hello"})
	if res.IsError {
		t.Fatalf("5xx capture failure must not be an error result: %+v", res)
	}
	want := obs.DegradedPhrase(obs.DegradedReasonCaptureFailed) + "\n\nhello\n"
	if got := resultText(t, res); got != want {
		t.Errorf("text = %q; want capture_failed (not session_lost): %q", got, want)
	}
	awaitSignal(t, eventsDone)
}

// A 401 on the initialize-time create latches auth_failed from the first tool
// call: the identical root cause (rejected credentials) phrases the same
// whether detected at initialize or mid-conversation, and no call ever
// misreports it as transient calm_unreachable. Only 401/403 latches — a 400
// at initialize is a config problem, not credentials, and keeps the generic
// degraded path.
func TestInitialize_CreateRejected_AuthFailedFromFirstCall(t *testing.T) {
	m := calm.NewMockClient(t)
	authErr := &calm.StatusError{Op: "create session", Code: 401, Status: "401 Unauthorized"}
	m.EXPECT().RegisterClient(mock.Anything, "claude-code").Return(false, authErr).Once()
	m.EXPECT().CreateSession(mock.Anything, "claude-code", 60, mock.Anything).Return("", authErr).Once()
	// Nothing else: no session to delete, and the latch blocks all CALM traffic.

	h := newHarness(t, m)
	initSession(t, h, "claude-code")

	res := callRunCommand(t, h, 2, map[string]any{"command": "echo hello"})
	if res.IsError {
		t.Fatalf("auth failure on an action tool must not be an error result: %+v", res)
	}
	want := obs.DegradedPhrase(obs.DegradedReasonAuthFailed) + "\n\nhello\n"
	if got := resultText(t, res); got != want {
		t.Errorf("text = %q; want auth_failed phrasing then raw: %q", got, want)
	}

	res = callSearch(t, h, 3, map[string]any{"queries": []string{"hello"}})
	if !res.IsError {
		t.Fatalf("auth-failed search must be an error result: %+v", res)
	}
	if got := resultText(t, res); got != obs.DegradedPhrase(obs.DegradedReasonAuthFailed) {
		t.Errorf("search text = %q; want auth_failed phrasing", got)
	}
}
