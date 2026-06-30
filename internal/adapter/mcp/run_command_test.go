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
	m.EXPECT().CreateSession(mock.Anything, "claude-code", 60).Return("tok-1", nil).Once()
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
	m.EXPECT().CreateSession(mock.Anything, "claude-code", 60).Return("tok-1", nil).Once()
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
	m.EXPECT().CreateSession(mock.Anything, "claude-code", 60).Return("tok-1", nil).Once()
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
	m.EXPECT().CreateSession(mock.Anything, "claude-code", 60).Return("tok-1", nil).Once()
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
	m.EXPECT().CreateSession(mock.Anything, "claude-code", 60).Return("tok-1", nil).Once()
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
	m.EXPECT().CreateSession(mock.Anything, "claude-code", 60).Return("tok-1", nil).Once()
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
	m.EXPECT().CreateSession(mock.Anything, "claude-code", 60).Return("tok-1", nil).Once()
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
	m.EXPECT().CreateSession(mock.Anything, "claude-code", 60).Return("tok-1", nil).Once()
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
	m.EXPECT().CreateSession(mock.Anything, "claude-code", 60).Return("tok-1", nil).Once()
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
	m.EXPECT().CreateSession(mock.Anything, "claude-code", 60).Return("tok-1", nil).Once()
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
