// Copyright 2026 The CALM Authors
// SPDX-License-Identifier: Apache-2.0

package middleware

import (
	"fmt"
	"net/http"

	"github.com/google/uuid"
	logging "github.com/one-harsh/context-logging"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/trace"

	"github.com/one-harsh/calm/internal/obs"
)

const (
	correlationIDHeader = "X-CALM-Correlation-Id"
	traceResponseHeader = "traceresponse"
)

// Context hydrates the request context with the server-minted correlation ID
// and inbound OTel trace context, sets the public observability headers on
// every response (including 4xx/5xx), and emits W3C traceresponse outbound
// whenever an inbound traceparent attached a valid span context.
func Context(logger *logging.Logger) func(http.Handler) http.Handler {
	propagator := otel.GetTextMapPropagator()
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ctx := propagator.Extract(r.Context(), propagation.HeaderCarrier(r.Header))

			if correlationID, err := uuid.NewV7(); err != nil {
				// Never-worse: mint failure must not block the value-producing call.
				// Skip the header; feedback for this call won't be possible (no id to
				// echo back), but the request itself completes normally.
				logger.WithContext(ctx).Warn(
					"context: UUIDv7 mint failed; response carries no correlation id",
					logging.ErrorField(err),
				)
			} else {
				correlationStr := correlationID.String()
				w.Header().Set(correlationIDHeader, correlationStr)
				ctx = logging.Bind(ctx, obs.CorrelationID(correlationStr))
				ctx = obs.WithCorrelationID(ctx, correlationID)
			}

			if sc := trace.SpanContextFromContext(ctx); sc.IsValid() {
				w.Header().Set(traceResponseHeader, fmt.Sprintf(
					"00-%s-%s-%02x",
					sc.TraceID().String(),
					sc.SpanID().String(),
					byte(sc.TraceFlags()),
				))
			}

			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}
