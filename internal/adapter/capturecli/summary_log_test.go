// Copyright 2026 The CALM Authors
// SPDX-License-Identifier: Apache-2.0

package capturecli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/mock"

	logging "github.com/one-harsh/context-logging"
	"github.com/one-harsh/context-logging/loggingtest"

	"github.com/one-harsh/calm/internal/adapter/calm"
	"github.com/one-harsh/calm/internal/adapter/obs"
)

func summaryCaptureLogger(t *testing.T) (*logging.Logger, *bytes.Buffer) {
	t.Helper()
	var buf bytes.Buffer
	l, err := logging.New(logging.Config{Level: "info", Format: "json", Output: &buf, Service: "test"})
	if err != nil {
		t.Fatalf("logger: %v", err)
	}
	return l, &buf
}

func depsWithLogger(t *testing.T, c calm.Client, log *logging.Logger) (Deps, *bytes.Buffer, *bytes.Buffer) {
	t.Helper()
	d, stdout, stderr := newDeps(t, c)
	d.Logger = log
	return d, stdout, stderr
}

func findSummary(t *testing.T, buf *bytes.Buffer, msg string) map[string]any {
	t.Helper()
	for _, e := range loggingtest.EntriesFromBytes(t, buf.Bytes()) {
		if e["msg"] == msg {
			return e
		}
	}
	t.Fatalf("no %q summary log entry; got:\n%s", msg, buf.String())
	return nil
}

func assertCanonicalSummary(t *testing.T, e map[string]any) {
	t.Helper()
	for _, k := range []string{
		obs.KeyWorkloadRequestID, "trace_id",
		obs.KeyCaptured, obs.KeyDegraded,
		obs.KeyPresentationMode, obs.KeyResponseVisibleBytes, obs.KeyResponseRawBytes,
		obs.KeyCallDurationMs,
	} {
		if _, ok := e[k]; !ok {
			t.Errorf("summary missing canonical field %q; got %v", k, e)
		}
	}
	if id, _ := e[obs.KeyWorkloadRequestID].(string); id == "" {
		t.Errorf("workload_request_id must be a non-empty join key; got %v", e[obs.KeyWorkloadRequestID])
	}
}

func visibleBytes(t *testing.T, e map[string]any) float64 {
	t.Helper()
	n, ok := e[obs.KeyResponseVisibleBytes].(float64)
	if !ok {
		t.Fatalf("visible_bytes missing or not numeric; got %v", e[obs.KeyResponseVisibleBytes])
	}
	return n
}

func TestExec_SummaryLogShape(t *testing.T) {
	c := calm.NewMockClient(t)
	expectEstablish(c, "tok1")
	log, buf := summaryCaptureLogger(t)
	d, _, _ := depsWithLogger(t, c, log)

	if code := Dispatch(context.Background(), d, execArgs("conv", "printf hello")); code != 0 {
		t.Fatalf("exit = %d; want 0", code)
	}
	e := findSummary(t, buf, "command completed")
	assertCanonicalSummary(t, e)
	if e[obs.KeyCaptured] != true {
		t.Errorf("captured = %v; want true", e[obs.KeyCaptured])
	}
	if e[obs.KeyDegraded] != false {
		t.Errorf("degraded = %v; want false", e[obs.KeyDegraded])
	}
	if vb := visibleBytes(t, e); vb <= 0 {
		t.Errorf("visible_bytes = %v; want > 0 (presentation reached stdout)", vb)
	}
}

func TestExec_SummaryLogShape_Degraded(t *testing.T) {
	c := calm.NewMockClient(t)
	c.EXPECT().RegisterClient(mock.Anything, mock.Anything).
		Return(false, &calm.StatusError{Op: "register", Code: 503, Status: "503 unavailable"}).Once()
	log, buf := summaryCaptureLogger(t)
	d, _, _ := depsWithLogger(t, c, log)

	Dispatch(context.Background(), d, execArgs("conv", "printf hello"))

	e := findSummary(t, buf, "command completed")
	assertCanonicalSummary(t, e)
	if e[obs.KeyDegraded] != true {
		t.Errorf("degraded = %v; want true", e[obs.KeyDegraded])
	}
	if _, ok := e[obs.KeyDegradedReason]; !ok {
		t.Errorf("degraded summary must carry %q; got %v", obs.KeyDegradedReason, e)
	}
	if vb := visibleBytes(t, e); vb <= 0 {
		t.Errorf("visible_bytes = %v; want > 0 (raw output still shown on capture failure)", vb)
	}
}

func TestSearch_SummaryLogShape(t *testing.T) {
	c := calm.NewMockClient(t)
	expectEstablish(c, "tok1")
	c.EXPECT().Search(mock.Anything, "tok1", mock.Anything).Return(calm.SearchResults{
		Queries: []calm.QueryResult{{Query: "needle", Hits: []calm.Hit{
			{Title: "t", Snippet: "found the needle here", Source: "src", MatchLayer: "primary"},
		}}},
		CorrelationID: "corr-1",
	}, nil).Once()
	log, buf := summaryCaptureLogger(t)
	d, _, _ := depsWithLogger(t, c, log)

	Dispatch(context.Background(), d, execArgs("conv", "printf hello"))
	if code := Dispatch(context.Background(), d, []string{"search", "--session", "conv", "needle"}); code != 0 {
		t.Fatalf("exit = %d; want 0", code)
	}
	e := findSummary(t, buf, "search completed")
	assertCanonicalSummary(t, e)
	if e[obs.KeyCaptured] != false {
		t.Errorf("captured = %v; want false (search does not capture)", e[obs.KeyCaptured])
	}
	if e[obs.KeyCorrelationID] != "corr-1" {
		t.Errorf("correlation_id = %v; want corr-1 (the join key to the correlations row)", e[obs.KeyCorrelationID])
	}
	raw, ok := e[obs.KeyResponseRawBytes].(float64)
	if !ok {
		t.Fatalf("search summary must carry raw bytes; got %v", e[obs.KeyResponseRawBytes])
	}
	if raw != visibleBytes(t, e) || raw <= 0 {
		t.Errorf("raw_bytes = %v; want == visible_bytes > 0 (verbatim search output)", raw)
	}
}

func TestSearch_SummaryLogShape_Degraded(t *testing.T) {
	c := calm.NewMockClient(t)
	expectEstablish(c, "tok1")
	log, buf := summaryCaptureLogger(t)
	d, _, _ := depsWithLogger(t, c, log)

	Dispatch(context.Background(), d, execArgs("conv", "printf hello"))
	code := Dispatch(context.Background(), d,
		[]string{"search", "--session", "conv", "source=calm:v1:file:read:ghost.go@zzzzzz", "q"})
	if code == 0 {
		t.Fatalf("stale source must exit nonzero")
	}
	e := findSummary(t, buf, "search completed")
	assertCanonicalSummary(t, e)
	if e[obs.KeyDegraded] != true {
		t.Errorf("degraded = %v; want true", e[obs.KeyDegraded])
	}
	if e[obs.KeyDegradedReason] != obs.DegradedReasonSessionLost {
		t.Errorf("degraded_reason = %v; want %q", e[obs.KeyDegradedReason], obs.DegradedReasonSessionLost)
	}
	if vb := visibleBytes(t, e); vb <= 0 {
		t.Errorf("visible_bytes = %v; want > 0 (degradation phrase reached the agent)", vb)
	}
}

func TestHook_SummaryLogShape(t *testing.T) {
	mc := calm.NewMockClient(t)
	expectEstablish(mc, "tok-obs")
	log, buf := summaryCaptureLogger(t)
	d, _, _ := depsWithLogger(t, mc, log)

	if _, code := dispatchHook(t, d, loadHookFixture(t, "claude_posttooluse_bash.json")); code != 0 {
		t.Fatalf("exit = %d; want 0", code)
	}
	e := findSummary(t, buf, "observation completed")
	assertCanonicalSummary(t, e)
	if e[obs.KeyCaptured] != true {
		t.Errorf("captured = %v; want true", e[obs.KeyCaptured])
	}
	if e[obs.KeyReplaced] != true {
		t.Errorf("replaced = %v; want true (the replacement wire was emitted)", e[obs.KeyReplaced])
	}
	if vb := visibleBytes(t, e); vb <= 0 {
		t.Errorf("visible_bytes = %v; want > 0 (replacement entered context)", vb)
	}
}

func TestHook_SummaryLogShape_Degraded(t *testing.T) {
	mc := calm.NewMockClient(t)
	mc.EXPECT().RegisterClient(mock.Anything, mock.Anything).Return(true, nil).Maybe()
	mc.EXPECT().CreateSession(mock.Anything, mock.Anything, mock.Anything, mock.Anything).Return("tok", nil).Once()
	mc.EXPECT().Ingest(mock.Anything, mock.Anything, mock.Anything).
		Return(calm.IngestSummary{}, errors.New("ingest boom")).Maybe()
	mc.EXPECT().WriteEvents(mock.Anything, mock.Anything, mock.Anything).Return(nil).Maybe()
	log, buf := summaryCaptureLogger(t)
	d, _, _ := depsWithLogger(t, mc, log)

	if _, code := dispatchHook(t, d, loadHookFixture(t, "claude_posttooluse_bash.json")); code != 0 {
		t.Fatalf("exit = %d; want 0", code)
	}
	e := findSummary(t, buf, "observation completed")
	assertCanonicalSummary(t, e)
	if e[obs.KeyDegraded] != true {
		t.Errorf("degraded = %v; want true", e[obs.KeyDegraded])
	}
	if _, ok := e[obs.KeyDegradedReason]; !ok {
		t.Errorf("degraded summary must carry %q; got %v", obs.KeyDegradedReason, e)
	}
	if e[obs.KeyReplaced] != false {
		t.Errorf("replaced = %v; want false (native result stood)", e[obs.KeyReplaced])
	}
	if e[obs.KeyPresentationMode] != obs.PresentationModeInline {
		t.Errorf("presentation mode = %v; want inline (nothing replaced the native output)", e[obs.KeyPresentationMode])
	}
	vb := visibleBytes(t, e)
	if vb <= 0 {
		t.Errorf("visible_bytes = %v; want > 0 (native payload stands in context)", vb)
	}
	if raw, ok := e[obs.KeyResponseRawBytes].(float64); !ok || raw != vb {
		t.Errorf("raw_bytes = %v; want == visible_bytes (native payload is both)", e[obs.KeyResponseRawBytes])
	}
}

func TestHook_SummaryLogShape_ReplaceUnsupported(t *testing.T) {
	mc := calm.NewMockClient(t)
	expectEstablish(mc, "tok-obs")
	log, buf := summaryCaptureLogger(t)
	d, _, _ := depsWithLogger(t, mc, log)

	var payload map[string]any
	if err := json.Unmarshal(loadHookFixture(t, "claude_posttooluse_bash.json"), &payload); err != nil {
		t.Fatalf("parse fixture: %v", err)
	}
	payload["hook_event_name"] = "PostToolUseFailure"
	payload["error"] = "boom: exit 2"
	patched, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("remarshal fixture: %v", err)
	}

	if _, code := dispatchHook(t, d, patched); code != 0 {
		t.Fatalf("exit = %d; want 0", code)
	}
	e := findSummary(t, buf, "observation completed")
	assertCanonicalSummary(t, e)
	if e[obs.KeyDegraded] != false {
		t.Errorf("degraded = %v; want false (capture-only is not degradation)", e[obs.KeyDegraded])
	}
	if e[obs.KeyReplaced] != false {
		t.Errorf("replaced = %v; want false (failure results are capture-only)", e[obs.KeyReplaced])
	}
	if e[obs.KeyPresentationMode] != obs.PresentationModeInline {
		t.Errorf("presentation mode = %v; want inline", e[obs.KeyPresentationMode])
	}
	vb := visibleBytes(t, e)
	if vb <= 0 {
		t.Errorf("visible_bytes = %v; want > 0 (native failure output stays in context)", vb)
	}
	if raw, ok := e[obs.KeyResponseRawBytes].(float64); !ok || raw != vb {
		t.Errorf("raw_bytes = %v; want == visible_bytes > 0", e[obs.KeyResponseRawBytes])
	}
}

func TestHook_SummaryLogShape_CompactReplacement(t *testing.T) {
	mc := calm.NewMockClient(t)
	expectEstablish(mc, "tok-obs")
	log, buf := summaryCaptureLogger(t)
	d, _, _ := depsWithLogger(t, mc, log)

	var payload map[string]any
	if err := json.Unmarshal(loadHookFixture(t, "claude_posttooluse_bash.json"), &payload); err != nil {
		t.Fatalf("parse fixture: %v", err)
	}
	tr, ok := payload["tool_response"].(map[string]any)
	if !ok {
		t.Fatalf("fixture missing tool_response object; got %v", payload["tool_response"])
	}
	tr["stdout"] = strings.Repeat("compact-path payload line\n", 160)
	patched, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("remarshal fixture: %v", err)
	}

	if _, code := dispatchHook(t, d, patched); code != 0 {
		t.Fatalf("exit = %d; want 0", code)
	}
	e := findSummary(t, buf, "observation completed")
	assertCanonicalSummary(t, e)
	if e[obs.KeyReplaced] != true {
		t.Errorf("replaced = %v; want true (the replacement wire was emitted)", e[obs.KeyReplaced])
	}
	if e[obs.KeyPresentationMode] != obs.PresentationModeSummary {
		t.Errorf("presentation mode = %v; want %q (a compact digest was emitted, not the payload)",
			e[obs.KeyPresentationMode], obs.PresentationModeSummary)
	}
	vb := visibleBytes(t, e)
	raw, rok := e[obs.KeyResponseRawBytes].(float64)
	if !rok {
		t.Fatalf("raw_bytes missing or not numeric; got %v", e[obs.KeyResponseRawBytes])
	}
	if vb <= 0 || raw <= vb {
		t.Errorf("raw/visible = %v/%v; want raw > visible > 0 (digest smaller than payload)", raw, vb)
	}
}

func TestFeedback_SummaryLogShape(t *testing.T) {
	const ref = "0195c2a6-7c4d-7e15-b3a1-0000000000aa"
	c := calm.NewMockClient(t)
	expectEstablish(c, "tok1")
	c.EXPECT().Feedback(mock.Anything, "tok1", ref, "success").Return(nil).Once()
	log, buf := summaryCaptureLogger(t)
	d, _, _ := depsWithLogger(t, c, log)

	Dispatch(context.Background(), d, execArgs("conv", "printf hello"))
	if code := Dispatch(context.Background(), d, []string{"feedback", "--session", "conv", ref, "success"}); code != 0 {
		t.Fatalf("exit = %d; want 0", code)
	}
	e := findSummary(t, buf, "feedback completed")
	assertCanonicalSummary(t, e)
	if e[obs.KeyCaptured] != false || e[obs.KeyDegraded] != false {
		t.Errorf("captured/degraded = %v/%v; want false/false", e[obs.KeyCaptured], e[obs.KeyDegraded])
	}
	if vb := visibleBytes(t, e); vb != 0 {
		t.Errorf("visible_bytes = %v; want 0 (success prints nothing)", vb)
	}
	if raw, ok := e[obs.KeyResponseRawBytes].(float64); !ok || raw != 0 {
		t.Errorf("raw_bytes = %v; want present and 0", e[obs.KeyResponseRawBytes])
	}
	if e[obs.KeyCorrelationID] != ref {
		t.Errorf("correlation_id = %v; want the ref (feedback joins its correlations row)", e[obs.KeyCorrelationID])
	}
}

func TestFeedback_SummaryLogShape_Degraded(t *testing.T) {
	c := calm.NewMockClient(t)
	log, buf := summaryCaptureLogger(t)
	d, _, _ := depsWithLogger(t, c, log)

	if code := Dispatch(context.Background(), d, []string{"feedback", "--session", "conv-fresh", "ref-1", "success"}); code == 0 {
		t.Fatal("feedback without a session must exit nonzero")
	}
	e := findSummary(t, buf, "feedback completed")
	assertCanonicalSummary(t, e)
	if e[obs.KeyDegraded] != true {
		t.Errorf("degraded = %v; want true", e[obs.KeyDegraded])
	}
	if e[obs.KeyDegradedReason] != obs.DegradedReasonCalmUnreachable {
		t.Errorf("degraded_reason = %v; want %q", e[obs.KeyDegradedReason], obs.DegradedReasonCalmUnreachable)
	}
	vb := visibleBytes(t, e)
	if vb <= 0 {
		t.Errorf("visible_bytes = %v; want > 0 (degradation phrase reached the agent)", vb)
	}
	if raw, ok := e[obs.KeyResponseRawBytes].(float64); !ok || raw != vb {
		t.Errorf("raw_bytes = %v; want == visible_bytes (phrase is the whole payload)", e[obs.KeyResponseRawBytes])
	}
}
