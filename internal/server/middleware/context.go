// Copyright 2026 The CALM Authors
// SPDX-License-Identifier: Apache-2.0

package middleware

import (
	"net/http"

	"github.com/google/uuid"
	logging "github.com/one-harsh/context-logging"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"
)

const requestIDHeader = "X-Request-ID"

// Context hydrates request context with RequestID and OTel trace context.
func Context() func(http.Handler) http.Handler {
	propagator := otel.GetTextMapPropagator()
	if propagator == nil {
		propagator = propagation.NewCompositeTextMapPropagator(
			propagation.TraceContext{},
			propagation.Baggage{},
		)
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ctx := propagator.Extract(r.Context(), propagation.HeaderCarrier(r.Header))

			requestID := r.Header.Get(requestIDHeader)
			if requestID == "" {
				requestID = uuid.NewString()
			}
			w.Header().Set(requestIDHeader, requestID)

			ctx = logging.Bind(ctx, logging.RequestID(requestID))
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}
