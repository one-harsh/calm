// Copyright 2026 The CALM Authors
// SPDX-License-Identifier: Apache-2.0

package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestTimeout_NonPositiveDurationIsPassthrough(t *testing.T) {
	for _, d := range []time.Duration{0, -time.Second, -time.Hour} {
		called := false
		next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			called = true
			w.WriteHeader(http.StatusOK)
		})
		req := httptest.NewRequest(http.MethodGet, "/x", nil)
		rec := httptest.NewRecorder()

		Timeout(d)(next).ServeHTTP(rec, req)

		if !called {
			t.Errorf("d=%v: downstream not called; want pass-through", d)
		}
		if rec.Code != http.StatusOK {
			t.Errorf("d=%v: status = %d; want 200", d, rec.Code)
		}
	}
}

func TestTimeout_FastHandlerCompletes(t *testing.T) {
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("done"))
	})
	req := httptest.NewRequest(http.MethodGet, "/x", nil)
	rec := httptest.NewRecorder()

	Timeout(time.Second)(next).ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("status = %d; want 200", rec.Code)
	}
	if rec.Body.String() != "done" {
		t.Errorf("body = %q; want done", rec.Body.String())
	}
}

func TestTimeout_SlowHandlerReturnsTimeoutResponse(t *testing.T) {
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	})
	req := httptest.NewRequest(http.MethodGet, "/x", nil)
	rec := httptest.NewRecorder()

	Timeout(10*time.Millisecond)(next).ServeHTTP(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("status = %d; want 503 (http.TimeoutHandler default)", rec.Code)
	}
	if got := rec.Body.String(); got != `{"error":"request timeout"}` {
		t.Errorf("body = %q; want timeout JSON", got)
	}
}
