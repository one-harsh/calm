// Copyright 2026 The CALM Authors
// SPDX-License-Identifier: Apache-2.0

package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"

	logging "github.com/one-harsh/context-logging"
)

func TestContext_GeneratesRequestIDWhenHeaderMissing(t *testing.T) {
	var observedID string
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		observedID = logging.RequestIDFromContext(r.Context())
		w.WriteHeader(http.StatusOK)
	})

	req := httptest.NewRequest(http.MethodGet, "/x", nil)
	rec := httptest.NewRecorder()
	Context()(next).ServeHTTP(rec, req)

	if observedID == "" {
		t.Fatal("downstream context had no RequestID")
	}
	if got := rec.Header().Get(requestIDHeader); got != observedID {
		t.Errorf("response %s header = %q; want %q (matches context)", requestIDHeader, got, observedID)
	}
}

func TestContext_PreservesIncomingRequestIDHeader(t *testing.T) {
	const incoming = "client-supplied-request-id-12345"
	var observedID string
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		observedID = logging.RequestIDFromContext(r.Context())
		w.WriteHeader(http.StatusOK)
	})

	req := httptest.NewRequest(http.MethodGet, "/x", nil)
	req.Header.Set(requestIDHeader, incoming)
	rec := httptest.NewRecorder()
	Context()(next).ServeHTTP(rec, req)

	if observedID != incoming {
		t.Errorf("context RequestID = %q; want %q (incoming header must be preserved)", observedID, incoming)
	}
	if got := rec.Header().Get(requestIDHeader); got != incoming {
		t.Errorf("response %s header = %q; want %q (echoed back to client)", requestIDHeader, got, incoming)
	}
}

func TestContext_GeneratedIDsAreUnique(t *testing.T) {
	seen := map[string]bool{}
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen[logging.RequestIDFromContext(r.Context())] = true
	})

	for i := 0; i < 50; i++ {
		req := httptest.NewRequest(http.MethodGet, "/x", nil)
		Context()(next).ServeHTTP(httptest.NewRecorder(), req)
	}
	if len(seen) != 50 {
		t.Errorf("uniqueness: got %d distinct ids across 50 requests; want 50", len(seen))
	}
}
