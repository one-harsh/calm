// Copyright 2026 The CALM Authors
// SPDX-License-Identifier: Apache-2.0

package obs_test

import (
	"context"
	"testing"

	"go.opentelemetry.io/otel/trace"

	"github.com/one-harsh/calm/internal/adapter/obs"
)

func TestWithCallContext_StampsTraceAndRequestID(t *testing.T) {
	ctx, id := obs.WithCallContext(context.Background())

	if id == "" {
		t.Fatal("WithCallContext returned an empty request id")
	}
	if got := obs.RequestIDFromContext(ctx); got != id {
		t.Errorf("RequestIDFromContext = %q; want the minted id %q", got, id)
	}
	if sc := trace.SpanContextFromContext(ctx); !sc.IsValid() {
		t.Error("WithCallContext did not stamp a valid trace context")
	}
}

func TestWithCallContext_DistinctPerCall(t *testing.T) {
	ctx1, id1 := obs.WithCallContext(context.Background())
	ctx2, id2 := obs.WithCallContext(context.Background())

	if id1 == id2 {
		t.Errorf("request ids collided: %q", id1)
	}
	if trace.SpanContextFromContext(ctx1).TraceID() == trace.SpanContextFromContext(ctx2).TraceID() {
		t.Error("trace ids collided across calls")
	}
}

func TestRequestIDFromContext_AbsentReturnsEmpty(t *testing.T) {
	if got := obs.RequestIDFromContext(context.Background()); got != "" {
		t.Errorf("RequestIDFromContext on a bare context = %q; want empty", got)
	}
}
