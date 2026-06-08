// Copyright 2026 The CALM Authors
// SPDX-License-Identifier: Apache-2.0

package middleware

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestWorkloadRequestID_EchoesValuePresent(t *testing.T) {
	const inbound = "wkld-req-abc-123"
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	req := httptest.NewRequest(http.MethodGet, "/x", nil)
	req.Header.Set(workloadRequestIDHeader, inbound)
	rec := httptest.NewRecorder()
	WorkloadRequestID()(next).ServeHTTP(rec, req)

	if got := rec.Header().Get(workloadRequestIDHeader); got != inbound {
		t.Errorf("response %s = %q; want %q (echoed)", workloadRequestIDHeader, got, inbound)
	}
	if rec.Code != http.StatusOK {
		t.Errorf("status = %d; want 200 (echo path)", rec.Code)
	}
}

func TestWorkloadRequestID_OmitsHeaderWhenInboundAbsent(t *testing.T) {
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	req := httptest.NewRequest(http.MethodGet, "/x", nil)
	rec := httptest.NewRecorder()
	WorkloadRequestID()(next).ServeHTTP(rec, req)

	if got := rec.Header().Get(workloadRequestIDHeader); got != "" {
		t.Errorf("response %s = %q; want empty (no inbound to echo)", workloadRequestIDHeader, got)
	}
}

func TestWorkloadRequestID_AcceptsValueAtBoundary(t *testing.T) {
	called := false
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	})

	boundary := strings.Repeat("a", 256)
	req := httptest.NewRequest(http.MethodGet, "/x", nil)
	req.Header.Set(workloadRequestIDHeader, boundary)
	rec := httptest.NewRecorder()
	WorkloadRequestID()(next).ServeHTTP(rec, req)

	if !called {
		t.Errorf("downstream handler did not run for %s of exactly 256 chars", workloadRequestIDHeader)
	}
	if got := rec.Header().Get(workloadRequestIDHeader); got != boundary {
		t.Errorf("response %s = %q; want 256-char value echoed", workloadRequestIDHeader, got)
	}
}

func TestWorkloadRequestID_RejectsOversizedWithFullChainCompletion(t *testing.T) {
	called := false
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	})

	req := httptest.NewRequest(http.MethodGet, "/x", nil)
	req.Header.Set(workloadRequestIDHeader, strings.Repeat("a", 257))
	rec := httptest.NewRecorder()
	WorkloadRequestID()(next).ServeHTTP(rec, req)

	if called {
		t.Errorf("downstream handler ran on oversized %s; middleware should short-circuit", workloadRequestIDHeader)
	}
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d; want 400 (oversized %s)", rec.Code, workloadRequestIDHeader)
	}
	if !strings.Contains(rec.Body.String(), "x_workload_request_id_too_long") {
		t.Errorf("body = %q; should contain x_workload_request_id_too_long", rec.Body.String())
	}
}
