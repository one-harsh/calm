// Copyright 2026 The CALM Authors
// SPDX-License-Identifier: Apache-2.0

package middleware

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"
	logging "github.com/one-harsh/context-logging"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"
)

func TestContext_MintsCorrelationIDOnEveryResponse(t *testing.T) {
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	req := httptest.NewRequest(http.MethodGet, "/x", nil)
	rec := httptest.NewRecorder()
	Context(logging.Nop())(next).ServeHTTP(rec, req)

	got := rec.Header().Get(correlationIDHeader)
	if got == "" {
		t.Fatalf("response missing %s header", correlationIDHeader)
	}
	parsed, err := uuid.Parse(got)
	if err != nil {
		t.Fatalf("%s = %q is not a valid UUID: %v", correlationIDHeader, got, err)
	}
	if parsed.Version() != 7 {
		t.Errorf("%s version = %d; want 7", correlationIDHeader, parsed.Version())
	}
}

func TestContext_CorrelationIDIsServerMintedNotInboundEchoed(t *testing.T) {
	const forged = "00000000-0000-7000-8000-000000000000"
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	req := httptest.NewRequest(http.MethodGet, "/x", nil)
	req.Header.Set(correlationIDHeader, forged)
	rec := httptest.NewRecorder()
	Context(logging.Nop())(next).ServeHTTP(rec, req)

	got := rec.Header().Get(correlationIDHeader)
	if got == forged {
		t.Errorf("response %s = %q; server should mint a fresh ID, not echo inbound", correlationIDHeader, got)
	}
}

func TestContext_DropsXRequestIDFromPublicSurface(t *testing.T) {
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	req := httptest.NewRequest(http.MethodGet, "/x", nil)
	rec := httptest.NewRecorder()
	Context(logging.Nop())(next).ServeHTTP(rec, req)

	if got := rec.Header().Get("X-Request-ID"); got != "" {
		t.Errorf("X-Request-ID = %q; want empty (replaced by %s)", got, correlationIDHeader)
	}
}

func TestContext_EmitsTraceResponseWhenInboundTraceparentValid(t *testing.T) {
	// Force the default W3C propagator so the trace context attaches.
	otel.SetTextMapPropagator(propagation.TraceContext{})

	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	req := httptest.NewRequest(http.MethodGet, "/x", nil)
	req.Header.Set("traceparent", "00-0af7651916cd43dd8448eb211c80319c-b9c7c989f97918e1-01")
	rec := httptest.NewRecorder()
	Context(logging.Nop())(next).ServeHTTP(rec, req)

	got := rec.Header().Get(traceResponseHeader)
	if got == "" {
		t.Fatalf("response missing %s when inbound traceparent attached a valid span context", traceResponseHeader)
	}
	if !strings.Contains(got, "0af7651916cd43dd8448eb211c80319c") {
		t.Errorf("%s = %q; should carry the inbound trace_id 0af7651916cd43dd8448eb211c80319c", traceResponseHeader, got)
	}
}

func TestContext_OmitsTraceResponseWhenNoInboundTraceparent(t *testing.T) {
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	req := httptest.NewRequest(http.MethodGet, "/x", nil)
	rec := httptest.NewRecorder()
	Context(logging.Nop())(next).ServeHTTP(rec, req)

	if got := rec.Header().Get(traceResponseHeader); got != "" {
		t.Errorf("response %s = %q; should be absent when no inbound traceparent (CALM does not start root spans in middleware today)", traceResponseHeader, got)
	}
}

func TestContext_GeneratedIDsAreUniqueAndSortable(t *testing.T) {
	seen := map[string]bool{}
	collected := []string{}
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	for i := 0; i < 50; i++ {
		req := httptest.NewRequest(http.MethodGet, "/x", nil)
		rec := httptest.NewRecorder()
		Context(logging.Nop())(next).ServeHTTP(rec, req)
		id := rec.Header().Get(correlationIDHeader)
		seen[id] = true
		collected = append(collected, id)
	}
	if len(seen) != 50 {
		t.Errorf("uniqueness: got %d distinct ids across 50 requests; want 50", len(seen))
	}
	// UUIDv7 timestamp prefix makes IDs roughly sortable. Strict monotonicity
	// isn't guaranteed within a millisecond, so only assert the loose property
	// that the first ID is not greater than the last.
	if strings.Compare(collected[0], collected[len(collected)-1]) > 0 {
		t.Errorf("UUIDv7 ids not sortable: first=%s last=%s", collected[0], collected[len(collected)-1])
	}
}
