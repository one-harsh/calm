// Copyright 2026 The CALM Authors
// SPDX-License-Identifier: Apache-2.0

package middleware

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	logging "github.com/one-harsh/context-logging"

	"github.com/one-harsh/calm/internal/auth"
	"github.com/one-harsh/calm/internal/db"
)

// stubClientResolver lets tests inject a deterministic token→client mapping
// without dragging in clientreg.Service + DAL machinery.
type stubClientResolver struct {
	tokens map[string]string // namespace+"|"+rawToken → client name
}

func (s *stubClientResolver) ResolveByToken(_ context.Context, ns, raw string) (string, error) {
	if name, ok := s.tokens[ns+"|"+raw]; ok {
		return name, nil
	}
	return "", db.ErrInvalidClientCredential
}

func (s *stubClientResolver) bind(ns, raw, name string) {
	if s.tokens == nil {
		s.tokens = map[string]string{}
	}
	s.tokens[ns+"|"+raw] = name
}

func okHandlerCalled(called *bool) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		*called = true
		w.WriteHeader(http.StatusOK)
	})
}

func rejectHandler(t *testing.T) http.Handler {
	return http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {
		t.Fatal("downstream handler should not be called")
	})
}

// ----- Uncredentialed namespaces (default: require_client_credentials=false) -----

func TestAuth_ValidAPIKey(t *testing.T) {
	reg := auth.NewMemoryRegistry(map[string]string{"goodkey": "production"}, nil, nil, nil, nil, nil)
	called := false
	req := httptest.NewRequest(http.MethodGet, "/v1/sessions/x", nil)
	req.Header.Set(auth.HeaderAPIKey, "goodkey")
	rec := httptest.NewRecorder()

	Auth(reg, &stubClientResolver{}, logging.Nop())(okHandlerCalled(&called)).ServeHTTP(rec, req)

	if !called {
		t.Fatal("downstream not called")
	}
	if rec.Code != http.StatusOK {
		t.Errorf("status = %d; want 200", rec.Code)
	}
}

func TestAuth_MissingAPIKeyHeader(t *testing.T) {
	reg := auth.NewMemoryRegistry(map[string]string{"goodkey": "production"}, nil, nil, nil, nil, nil)
	req := httptest.NewRequest(http.MethodGet, "/v1/sessions/x", nil)
	rec := httptest.NewRecorder()

	Auth(reg, &stubClientResolver{}, logging.Nop())(rejectHandler(t)).ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Errorf("status = %d; want 401", rec.Code)
	}
}

func TestAuth_UnknownAPIKey(t *testing.T) {
	reg := auth.NewMemoryRegistry(map[string]string{"goodkey": "production"}, nil, nil, nil, nil, nil)
	req := httptest.NewRequest(http.MethodGet, "/v1/sessions/x", nil)
	req.Header.Set(auth.HeaderAPIKey, "wrongkey")
	rec := httptest.NewRecorder()

	Auth(reg, &stubClientResolver{}, logging.Nop())(rejectHandler(t)).ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Errorf("status = %d; want 401", rec.Code)
	}
}

func TestAuth_UncredentialedIgnoresAuthorizationHeader(t *testing.T) {
	// Uncredentialed namespace: stray Authorization: Bearer header is
	// ignored, not rejected. Workloads might present one by mistake or
	// because they're configured for a credentialed namespace elsewhere.
	reg := auth.NewMemoryRegistry(map[string]string{"goodkey": "production"}, nil, nil, nil, nil, nil)
	called := false
	req := httptest.NewRequest(http.MethodGet, "/v1/sessions/x", nil)
	req.Header.Set(auth.HeaderAPIKey, "goodkey")
	req.Header.Set(auth.HeaderAuthorization, auth.BearerPrefix+"some-random-token")
	rec := httptest.NewRecorder()

	Auth(reg, &stubClientResolver{}, logging.Nop())(okHandlerCalled(&called)).ServeHTTP(rec, req)

	if !called || rec.Code != http.StatusOK {
		t.Errorf("uncredentialed namespace should ignore Authorization: got status=%d called=%v", rec.Code, called)
	}
}

// ----- Credentialed namespaces (require_client_credentials=true) — Authorization: Bearer <client-token> required -----

func TestAuth_Credentialed_ValidClientToken(t *testing.T) {
	reg := auth.NewMemoryRegistry(
		map[string]string{"goodkey": "production"},
		nil,
		map[string]bool{"production": true},
		nil,
		nil,
		nil,
	)
	resolver := &stubClientResolver{}
	resolver.bind("production", "ct-abc", "factory-pipeline")

	called := false
	req := httptest.NewRequest(http.MethodPost, "/v1/sessions", nil)
	req.Header.Set(auth.HeaderAPIKey, "goodkey")
	req.Header.Set(auth.HeaderAuthorization, auth.BearerPrefix+"ct-abc")
	rec := httptest.NewRecorder()

	Auth(reg, resolver, logging.Nop())(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		if got := auth.NamespaceFromContext(r.Context()); got != "production" {
			t.Errorf("namespace ctx = %q; want production", got)
		}
		if got := auth.ClientFromContext(r.Context()); got != "factory-pipeline" {
			t.Errorf("client ctx = %q; want factory-pipeline", got)
		}
		w.WriteHeader(http.StatusOK)
	})).ServeHTTP(rec, req)

	if !called || rec.Code != http.StatusOK {
		t.Errorf("credentialed valid: got status=%d called=%v", rec.Code, called)
	}
}

func TestAuth_Credentialed_MissingClientToken(t *testing.T) {
	reg := auth.NewMemoryRegistry(
		map[string]string{"goodkey": "production"},
		nil,
		map[string]bool{"production": true},
		nil,
		nil,
		nil,
	)
	req := httptest.NewRequest(http.MethodPost, "/v1/sessions", nil)
	req.Header.Set(auth.HeaderAPIKey, "goodkey")
	// No Authorization header.
	rec := httptest.NewRecorder()

	Auth(reg, &stubClientResolver{}, logging.Nop())(rejectHandler(t)).ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Errorf("status = %d; want 401", rec.Code)
	}
}

func TestAuth_Credentialed_InvalidClientToken(t *testing.T) {
	reg := auth.NewMemoryRegistry(
		map[string]string{"goodkey": "production"},
		nil,
		map[string]bool{"production": true},
		nil,
		nil,
		nil,
	)
	resolver := &stubClientResolver{}
	// No tokens bound — every lookup fails.
	req := httptest.NewRequest(http.MethodPost, "/v1/sessions", nil)
	req.Header.Set(auth.HeaderAPIKey, "goodkey")
	req.Header.Set(auth.HeaderAuthorization, auth.BearerPrefix+"not-a-real-token")
	rec := httptest.NewRecorder()

	Auth(reg, resolver, logging.Nop())(rejectHandler(t)).ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Errorf("status = %d; want 401", rec.Code)
	}
}

func TestAuth_Credentialed_RegistrationPathExempt(t *testing.T) {
	// POST /v1/clients/{name} is exempt from the client-token check (the
	// client doesn't have a token yet). API key alone authenticates.
	reg := auth.NewMemoryRegistry(
		map[string]string{"goodkey": "production"},
		nil,
		map[string]bool{"production": true},
		nil,
		nil,
		nil,
	)
	called := false
	req := httptest.NewRequest(http.MethodPost, "/v1/clients/new-client", nil)
	req.Header.Set(auth.HeaderAPIKey, "goodkey")
	rec := httptest.NewRecorder()

	Auth(reg, &stubClientResolver{}, logging.Nop())(okHandlerCalled(&called)).ServeHTTP(rec, req)

	if !called || rec.Code != http.StatusOK {
		t.Errorf("registration exempt: got status=%d called=%v", rec.Code, called)
	}
}

func TestAuth_Credentialed_RotateTokenStillRequiresToken(t *testing.T) {
	// POST /v1/clients/{name}/rotate-token is NOT exempt — rotation requires
	// the current token. Has further path segments past /v1/clients/{name}/,
	// so isClientRegistration returns false.
	reg := auth.NewMemoryRegistry(
		map[string]string{"goodkey": "production"},
		nil,
		map[string]bool{"production": true},
		nil,
		nil,
		nil,
	)
	req := httptest.NewRequest(http.MethodPost, "/v1/clients/foo/rotate-token", nil)
	req.Header.Set(auth.HeaderAPIKey, "goodkey")
	rec := httptest.NewRecorder()

	Auth(reg, &stubClientResolver{}, logging.Nop())(rejectHandler(t)).ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Errorf("rotate without token: got %d; want 401", rec.Code)
	}
}

// ----- Credentialed namespaces: token presentation edge cases -----

func TestAuth_Credentialed_NonBearerScheme(t *testing.T) {
	reg := auth.NewMemoryRegistry(
		map[string]string{"goodkey": "production"},
		nil,
		map[string]bool{"production": true},
		nil,
		nil,
		nil,
	)
	req := httptest.NewRequest(http.MethodPost, "/v1/sessions", nil)
	req.Header.Set(auth.HeaderAPIKey, "goodkey")
	req.Header.Set(auth.HeaderAuthorization, "Basic Z29vZGtleQ==")
	rec := httptest.NewRecorder()

	Auth(reg, &stubClientResolver{}, logging.Nop())(rejectHandler(t)).ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Errorf("status = %d; want 401", rec.Code)
	}
}

func TestAuth_Credentialed_LowercaseBearerRejected(t *testing.T) {
	// HasPrefix is case-sensitive. CALM's adapter emits canonical "Bearer ".
	reg := auth.NewMemoryRegistry(
		map[string]string{"goodkey": "production"},
		nil,
		map[string]bool{"production": true},
		nil,
		nil,
		nil,
	)
	resolver := &stubClientResolver{}
	resolver.bind("production", "ct-abc", "factory")
	req := httptest.NewRequest(http.MethodPost, "/v1/sessions", nil)
	req.Header.Set(auth.HeaderAPIKey, "goodkey")
	req.Header.Set(auth.HeaderAuthorization, "bearer ct-abc")
	rec := httptest.NewRecorder()

	Auth(reg, resolver, logging.Nop())(rejectHandler(t)).ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Errorf("status = %d; want 401", rec.Code)
	}
}

// ----- Unauthenticated paths -----

func TestAuth_HealthExempt(t *testing.T) {
	reg := auth.NewMemoryRegistry(map[string]string{"goodkey": "production"}, nil, nil, nil, nil, nil)
	for _, path := range []string{"/v1/health", "/v1/version"} {
		called := false
		req := httptest.NewRequest(http.MethodGet, path, nil)
		// No API key.
		rec := httptest.NewRecorder()
		Auth(reg, &stubClientResolver{}, logging.Nop())(okHandlerCalled(&called)).ServeHTTP(rec, req)
		if !called || rec.Code != http.StatusOK {
			t.Errorf("%s: status=%d called=%v; want 200/true", path, rec.Code, called)
		}
	}
}

var _ ClientResolver = (*stubClientResolver)(nil)
