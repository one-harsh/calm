// Copyright 2026 The CALM Authors
// SPDX-License-Identifier: Apache-2.0

package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// RateLimit is a documented placeholder until per-namespace limiting lands.
// This test pins the current pass-through behavior so the WI that ships the
// real impl is forced to update the assertion (and bring its own tests).
func TestRateLimit_PlaceholderIsPassthrough(t *testing.T) {
	for _, perSec := range []int{0, 1, 100, -1} {
		called := false
		next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			called = true
			w.WriteHeader(http.StatusOK)
		})
		req := httptest.NewRequest(http.MethodGet, "/x", nil)
		rec := httptest.NewRecorder()

		RateLimit(perSec)(next).ServeHTTP(rec, req)

		if !called {
			t.Errorf("perSec=%d: downstream not called", perSec)
		}
		if rec.Code != http.StatusOK {
			t.Errorf("perSec=%d: status = %d; want 200", perSec, rec.Code)
		}
	}
}
