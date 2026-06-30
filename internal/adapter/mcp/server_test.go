// Copyright 2026 The CALM Authors
// SPDX-License-Identifier: Apache-2.0

package mcp_test

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"sync"
	"testing"
	"time"

	logging "github.com/one-harsh/context-logging"
	"github.com/one-harsh/context-logging/loggingtest"
	"github.com/stretchr/testify/mock"

	"github.com/one-harsh/calm/internal/adapter/calm"
	"github.com/one-harsh/calm/internal/adapter/mcp"
	"github.com/one-harsh/calm/internal/adapter/obs"
)

type rpcResp struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  json.RawMessage `json:"result"`
	Error   *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

type harness struct {
	t        *testing.T
	inW      *io.PipeWriter
	outR     *bufio.Reader
	done     chan error
	cancel   context.CancelFunc
	waitOnce sync.Once
	err      error
}

func discardLogger(t *testing.T) *logging.Logger {
	t.Helper()
	l, err := logging.New(logging.Config{Level: "error", Format: "json", Output: io.Discard, Service: "test"})
	if err != nil {
		t.Fatalf("logger: %v", err)
	}
	return l
}

func newHarness(t *testing.T, client calm.Client, tools ...mcp.Tool) *harness {
	t.Helper()
	return newHarnessWithLogger(t, discardLogger(t), client, tools...)
}

// newHarnessWithLogger wires a harness against a caller-supplied logger so
// tests asserting on log output can inspect emissions.
func newHarnessWithLogger(t *testing.T, log *logging.Logger, client calm.Client, tools ...mcp.Tool) *harness {
	t.Helper()
	srv := mcp.NewServer(mcp.Config{
		Calm:              client,
		Logger:            log,
		ServerName:        "calm-adapter",
		ServerVersion:     "test",
		DefaultClient:     "calm-adapter",
		SessionTTLMinutes: 60,
		Tools:             tools,
	})
	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- srv.Serve(ctx, inR, outW) }()
	h := &harness{t: t, inW: inW, outR: bufio.NewReader(outR), done: done, cancel: cancel}
	t.Cleanup(func() {
		cancel()
		_ = inW.Close()
		h.wait()
	})
	return h
}

func captureLogger(t *testing.T) (*logging.Logger, *bytes.Buffer) {
	t.Helper()
	var buf bytes.Buffer
	l, err := logging.New(logging.Config{Level: "info", Format: "json", Output: &buf, Service: "test"})
	if err != nil {
		t.Fatalf("logger: %v", err)
	}
	return l, &buf
}

func (h *harness) send(v any) {
	h.t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		h.t.Fatalf("marshal: %v", err)
	}
	if _, err := h.inW.Write(append(b, '\n')); err != nil {
		h.t.Fatalf("send: %v", err)
	}
}

func (h *harness) sendRaw(line string) {
	h.t.Helper()
	if _, err := h.inW.Write([]byte(line)); err != nil {
		h.t.Fatalf("sendRaw: %v", err)
	}
}

func (h *harness) recv() rpcResp {
	h.t.Helper()
	line, err := h.outR.ReadBytes('\n')
	if err != nil {
		h.t.Fatalf("recv: %v", err)
	}
	var r rpcResp
	if err := json.Unmarshal(line, &r); err != nil {
		h.t.Fatalf("decode response: %v", err)
	}
	return r
}

func (h *harness) wait() error {
	h.waitOnce.Do(func() {
		select {
		case h.err = <-h.done:
		case <-time.After(3 * time.Second):
			h.err = errors.New("Serve did not exit")
		}
	})
	return h.err
}

func req(id int, method string, params any) map[string]any {
	m := map[string]any{"jsonrpc": "2.0", "id": id, "method": method}
	if params != nil {
		m["params"] = params
	}
	return m
}

func stubTool(name string) mcp.Tool {
	return mcp.Tool{
		Name:        name,
		Description: "stub",
		InputSchema: json.RawMessage(`{"type":"object"}`),
		Handler: func(_ context.Context, _ json.RawMessage) (mcp.ToolResult, error) {
			return mcp.TextResult("ran "+name, false), nil
		},
	}
}

func TestInitialize_CreatesSessionAndReturnsServerInfo(t *testing.T) {
	m := calm.NewMockClient(t)
	m.EXPECT().RegisterClient(mock.Anything, "claude-code").Return(true, nil).Once()
	m.EXPECT().CreateSession(mock.Anything, "claude-code", 60).Return("tok-1", nil).Once()
	m.EXPECT().DeleteSession(mock.Anything, "tok-1").Return(nil).Once()
	h := newHarness(t, m)

	h.send(req(1, "initialize", map[string]any{
		"protocolVersion": "2025-06-18",
		"clientInfo":      map[string]any{"name": "claude-code", "version": "1.2.3"},
	}))
	r := h.recv()
	if r.Error != nil {
		t.Fatalf("initialize error: %+v", r.Error)
	}
	var res struct {
		ProtocolVersion string                `json:"protocolVersion"`
		Capabilities    map[string]any        `json:"capabilities"`
		ServerInfo      struct{ Name string } `json:"serverInfo"`
	}
	if err := json.Unmarshal(r.Result, &res); err != nil {
		t.Fatalf("decode result: %v", err)
	}
	if res.ServerInfo.Name != "calm-adapter" {
		t.Errorf("serverInfo.name = %q; want calm-adapter", res.ServerInfo.Name)
	}
	if res.ProtocolVersion != "2025-06-18" {
		t.Errorf("protocolVersion = %q; want echoed 2025-06-18", res.ProtocolVersion)
	}
	if _, ok := res.Capabilities["tools"]; !ok {
		t.Errorf("capabilities missing tools: %+v", res.Capabilities)
	}
}

func TestToolsListAndCall(t *testing.T) {
	h := newHarness(t, calm.NewMockClient(t), stubTool("calm_run_command"), stubTool("calm_search"))

	h.send(req(1, "tools/list", nil))
	r := h.recv()
	var list struct {
		Tools []struct{ Name string } `json:"tools"`
	}
	if err := json.Unmarshal(r.Result, &list); err != nil {
		t.Fatalf("decode tools/list: %v", err)
	}
	if len(list.Tools) != 2 || list.Tools[0].Name != "calm_run_command" || list.Tools[1].Name != "calm_search" {
		t.Fatalf("tools = %+v; want [calm_run_command calm_search]", list.Tools)
	}

	h.send(req(2, "tools/call", map[string]any{"name": "calm_run_command", "arguments": map[string]any{}}))
	r = h.recv()
	var called mcp.ToolResult
	if err := json.Unmarshal(r.Result, &called); err != nil {
		t.Fatalf("decode tools/call: %v", err)
	}
	if called.IsError || len(called.Content) != 1 || called.Content[0].Text != "ran calm_run_command" {
		t.Fatalf("tool result = %+v; want non-error text", called)
	}
}

func TestUnknownToolIsErrorResult(t *testing.T) {
	h := newHarness(t, calm.NewMockClient(t))
	h.send(req(1, "tools/call", map[string]any{"name": "nope", "arguments": map[string]any{}}))
	r := h.recv()
	if r.Error != nil {
		t.Fatalf("want isError result, got protocol error: %+v", r.Error)
	}
	var res mcp.ToolResult
	if err := json.Unmarshal(r.Result, &res); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !res.IsError {
		t.Errorf("unknown tool should yield isError result: %+v", res)
	}
}

func TestToolHandlerErrorBecomesIsErrorResult(t *testing.T) {
	errTool := mcp.Tool{
		Name:        "boom",
		InputSchema: json.RawMessage(`{}`),
		Handler: func(context.Context, json.RawMessage) (mcp.ToolResult, error) {
			return mcp.ToolResult{}, errors.New("kaboom")
		},
	}
	h := newHarness(t, calm.NewMockClient(t), errTool)
	h.send(req(1, "tools/call", map[string]any{"name": "boom", "arguments": map[string]any{}}))
	var res mcp.ToolResult
	if err := json.Unmarshal(h.recv().Result, &res); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !res.IsError || len(res.Content) == 0 || res.Content[0].Text != "kaboom" {
		t.Fatalf("want isError result carrying 'kaboom', got %+v", res)
	}
}

func TestToolHandlerPanicIsRecovered(t *testing.T) {
	panicTool := mcp.Tool{
		Name:        "boom",
		InputSchema: json.RawMessage(`{}`),
		Handler: func(context.Context, json.RawMessage) (mcp.ToolResult, error) {
			panic("oh no")
		},
	}
	h := newHarness(t, calm.NewMockClient(t), panicTool)
	h.send(req(1, "tools/call", map[string]any{"name": "boom", "arguments": map[string]any{}}))
	var res mcp.ToolResult
	if err := json.Unmarshal(h.recv().Result, &res); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !res.IsError {
		t.Fatalf("panic must yield an isError result (never crash), got %+v", res)
	}
}

func TestUnknownMethodReturnsMethodNotFound(t *testing.T) {
	h := newHarness(t, calm.NewMockClient(t))
	h.send(req(1, "does/notexist", nil))
	r := h.recv()
	if r.Error == nil || r.Error.Code != -32601 {
		t.Fatalf("want method-not-found (-32601), got %+v", r.Error)
	}
}

func TestNotificationProducesNoResponse(t *testing.T) {
	h := newHarness(t, calm.NewMockClient(t))
	// Notification (no id) must yield nothing; the following ping's response
	// is what we read back, proving the notification produced no output.
	h.send(map[string]any{"jsonrpc": "2.0", "method": "notifications/initialized"})
	h.send(req(7, "ping", nil))
	r := h.recv()
	if string(r.ID) != "7" {
		t.Fatalf("first response id = %s; want 7 (notification must produce no response)", r.ID)
	}
}

func TestDuplicateInitializeReusesSession(t *testing.T) {
	m := calm.NewMockClient(t)
	m.EXPECT().RegisterClient(mock.Anything, "claude-code").Return(true, nil).Once()
	m.EXPECT().CreateSession(mock.Anything, "claude-code", 60).Return("tok1", nil).Once()
	m.EXPECT().DeleteSession(mock.Anything, "tok1").Return(nil).Once()
	h := newHarness(t, m)

	for i := 1; i <= 2; i++ {
		h.send(req(i, "initialize", map[string]any{"clientInfo": map[string]any{"name": "claude-code"}}))
		if r := h.recv(); r.Error != nil {
			t.Fatalf("initialize #%d error: %+v", i, r.Error)
		}
	}
	// Assertions live in the mock: CreateSession.Once() (no second session) +
	// DeleteSession.Once() on cleanup (the single token is torn down — no orphan).
}

func TestParseErrorResponseCarriesNullID(t *testing.T) {
	h := newHarness(t, calm.NewMockClient(t))
	h.sendRaw("{not valid json}\n")
	r := h.recv()
	if r.Error == nil || r.Error.Code != -32700 {
		t.Fatalf("want parse error (-32700), got %+v", r.Error)
	}
	if string(r.ID) != "null" {
		t.Fatalf("parse-error id = %q; want null (JSON-RPC requires id present, null when unreadable)", r.ID)
	}
}

func TestNullIDIsRequestNotNotification(t *testing.T) {
	h := newHarness(t, calm.NewMockClient(t))
	h.send(map[string]any{"jsonrpc": "2.0", "id": nil, "method": "ping"})
	r := h.recv()
	if string(r.ID) != "null" {
		t.Fatalf("id:null response id = %q; want null", r.ID)
	}
	if r.Error != nil {
		t.Fatalf("ping with id:null errored: %+v", r.Error)
	}
}

// The per-call summary log fires once at the end of invokeTool with the
// canonical field set per DESIGN.md §7: identity (tool, workload_request_id),
// trace_id (auto from OTel SpanContext), categorical status defaulted by
// invokeTool and overridable by the handler via BindSummary, measurement
// fields computed at drain, and presentation.mode hardcoded "summary" until
// AI-04.
func TestInvokeTool_SummaryLogShape(t *testing.T) {
	summaryTool := mcp.Tool{
		Name:        "summary_probe",
		InputSchema: json.RawMessage(`{}`),
		Handler: func(ctx context.Context, _ json.RawMessage) (mcp.ToolResult, error) {
			_ = logging.BindSummary(
				ctx,
				logging.BoolField(obs.KeyCaptured, true),
				obs.SourceLabel("calm:v1:test:probe"),
				obs.ResponseRawBytes(42),
			)
			return mcp.TextResult("ok", false), nil
		},
	}
	log, buf := captureLogger(t)
	h := newHarnessWithLogger(t, log, calm.NewMockClient(t), summaryTool)

	h.send(req(1, "tools/call", map[string]any{"name": "summary_probe", "arguments": map[string]any{}}))
	if r := h.recv(); r.Error != nil {
		t.Fatalf("tool/call protocol error: %+v", r.Error)
	}
	// Cancel the harness so the deferred summary log fully drains before we read the buffer.
	h.cancel()
	_ = h.inW.Close()
	h.wait()

	entries := loggingtest.EntriesFromBytes(t, buf.Bytes())
	var summary map[string]any
	for _, e := range entries {
		if e["msg"] == "tool call completed" {
			summary = e
			break
		}
	}
	if summary == nil {
		t.Fatalf("no 'tool call completed' log entry found; entries=%v", entries)
	}
	requiredKeys := []string{
		"tool", "workload_request_id", "trace_id",
		"captured", "degraded",
		obs.KeySourceLabel,
		obs.KeyResponseVisibleBytes, obs.KeyResponseRawBytes, obs.KeyCallDurationMs, obs.KeyPresentationMode,
	}
	for _, k := range requiredKeys {
		if _, ok := summary[k]; !ok {
			t.Errorf("summary log missing %q; got fields=%v", k, fieldKeys(summary))
		}
	}
	if got := summary["tool"]; got != "summary_probe" {
		t.Errorf("tool field = %v; want summary_probe", got)
	}
	if got := summary["captured"]; got != true {
		t.Errorf("captured = %v; want true (overridden by handler)", got)
	}
	if got := summary["degraded"]; got != false {
		t.Errorf("degraded = %v; want false (default; handler didn't override)", got)
	}
	if got := summary[obs.KeyPresentationMode]; got != obs.PresentationModeSummary {
		t.Errorf("presentation.mode = %v; want %q", got, obs.PresentationModeSummary)
	}
}

func fieldKeys(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	return keys
}

func TestEOFExitsCleanly(t *testing.T) {
	h := newHarness(t, calm.NewMockClient(t))
	_ = h.inW.Close()
	if err := h.wait(); err != nil {
		t.Fatalf("Serve on EOF = %v; want nil", err)
	}
}
