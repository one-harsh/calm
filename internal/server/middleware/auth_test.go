// Copyright 2026 The CALM Authors
// SPDX-License-Identifier: Apache-2.0

package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/one-harsh/calm/internal/auth"
)

func TestAuth_BearerValid(t *testing.T) {
	reg := auth.NewMemoryRegistry(
		map[string]string{"goodkey": "production"},
		nil,
	)
	called := false
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	})

	req := httptest.NewRequest(http.MethodGet, "/v1/sessions/x", nil)
	req.Header.Set("Authorization", "Bearer goodkey")
	rec := httptest.NewRecorder()

	Auth(reg)(next).ServeHTTP(rec, req)

	if !called {
		t.Fatal("downstream handler not called")
	}
	if rec.Code != http.StatusOK {
		t.Errorf("status = %d; want 200", rec.Code)
	}
}

func TestAuth_MissingAuthorizationHeader(t *testing.T) {
	reg := auth.NewMemoryRegistry(map[string]string{"goodkey": "production"}, nil)
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("downstream handler should not be called")
	})

	req := httptest.NewRequest(http.MethodGet, "/v1/sessions/x", nil)
	rec := httptest.NewRecorder()

	Auth(reg)(next).ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Errorf("status = %d; want 401", rec.Code)
	}
}

func TestAuth_NonBearerScheme(t *testing.T) {
	reg := auth.NewMemoryRegistry(map[string]string{"goodkey": "production"}, nil)
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("downstream handler should not be called")
	})

	req := httptest.NewRequest(http.MethodGet, "/v1/sessions/x", nil)
	req.Header.Set("Authorization", "Basic Z29vZGtleQ==")
	rec := httptest.NewRecorder()

	Auth(reg)(next).ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Errorf("status = %d; want 401", rec.Code)
	}
}

func TestAuth_UnknownBearerKey(t *testing.T) {
	reg := auth.NewMemoryRegistry(map[string]string{"goodkey": "production"}, nil)
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("downstream handler should not be called")
	})

	req := httptest.NewRequest(http.MethodGet, "/v1/sessions/x", nil)
	req.Header.Set("Authorization", "Bearer wrongkey")
	rec := httptest.NewRecorder()

	Auth(reg)(next).ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Errorf("status = %d; want 401", rec.Code)
	}
}

func TestAuth_BearerPrefixCaseSensitive(t *testing.T) {
	// HTTP scheme tokens are case-insensitive per RFC, but strings.HasPrefix is
	// case-sensitive. Document the behavior: lowercase "bearer " is rejected.
	// If we want case-insensitive matching, change the middleware. For now
	// CALM's adapter and SDKs emit canonical "Bearer ".
	reg := auth.NewMemoryRegistry(map[string]string{"goodkey": "production"}, nil)
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("downstream handler should not be called for lowercase bearer prefix")
	})

	req := httptest.NewRequest(http.MethodGet, "/v1/sessions/x", nil)
	req.Header.Set("Authorization", "bearer goodkey")
	rec := httptest.NewRecorder()

	Auth(reg)(next).ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Errorf("status = %d; want 401", rec.Code)
	}
}
