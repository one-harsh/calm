// Copyright 2026 The CALM Authors
// SPDX-License-Identifier: Apache-2.0

package obs

import (
	"context"
	"crypto/rand"
	"encoding/hex"

	"go.opentelemetry.io/otel/trace"
)

type ctxKey int

const requestIDKey ctxKey = iota

// WithCallContext stamps a per-call identity onto ctx: a sampled W3C trace context (so
// adapter logs carry trace_id/span_id and outbound CALM requests carry traceparent) and a
// short workload request id.
//
// Propagation-only — no OTel SDK/spans; just correlation that joins adapter logs ↔ CALM logs.
func WithCallContext(ctx context.Context) (context.Context, string) {
	var tid trace.TraceID
	var sid trace.SpanID
	_, _ = rand.Read(tid[:])
	_, _ = rand.Read(sid[:])
	sc := trace.NewSpanContext(trace.SpanContextConfig{
		TraceID:    tid,
		SpanID:     sid,
		TraceFlags: trace.FlagsSampled,
	})
	ctx = trace.ContextWithSpanContext(ctx, sc)

	reqID := newRequestID()
	ctx = context.WithValue(ctx, requestIDKey, reqID)
	return ctx, reqID
}

func RequestIDFromContext(ctx context.Context) string {
	id, _ := ctx.Value(requestIDKey).(string)
	return id
}

func newRequestID() string {
	var b [12]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}
