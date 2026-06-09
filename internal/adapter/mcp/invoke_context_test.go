// Copyright 2026 The CALM Authors
// SPDX-License-Identifier: Apache-2.0

package mcp_test

import (
	"context"
	"encoding/json"
	"sync"
	"testing"

	"go.opentelemetry.io/otel/trace"

	"github.com/one-harsh/calm/internal/adapter/calm"
	"github.com/one-harsh/calm/internal/adapter/mcp"
)

// invokeTool stamps a per-call trace context onto the handler's ctx, so every tool's
// downstream logs + CALM calls carry a trace id. (The workload request id rides the same
// ctx and is asserted end-to-end in the calm package's threading test.)
func TestInvokeTool_StampsCallTraceContext(t *testing.T) {
	var mu sync.Mutex
	var got trace.SpanContext
	capture := mcp.Tool{
		Name:        "capture",
		InputSchema: json.RawMessage(`{}`),
		Handler: func(ctx context.Context, _ json.RawMessage) (mcp.ToolResult, error) {
			mu.Lock()
			got = trace.SpanContextFromContext(ctx)
			mu.Unlock()
			return mcp.TextResult("ok", false), nil
		},
	}

	h := newHarness(t, calm.NewMockClient(t), capture)
	h.send(req(1, "tools/call", map[string]any{"name": "capture", "arguments": map[string]any{}}))
	if r := h.recv(); r.Error != nil {
		t.Fatalf("tools/call: %+v", r.Error)
	}

	mu.Lock()
	defer mu.Unlock()
	if !got.IsValid() {
		t.Fatal("invokeTool did not stamp a valid trace context onto the handler ctx")
	}
}
