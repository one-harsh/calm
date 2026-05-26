// Copyright 2026 The CALM Authors
// SPDX-License-Identifier: Apache-2.0

package middleware

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/getkin/kin-openapi/openapi3"
)

// minimalSpec returns a tiny in-memory OpenAPI 3 spec with one endpoint that
// requires `?q` as a non-empty query parameter and declares an apiKey
// security scheme. Used to drive the kin-openapi wrapper behavior under test
// without depending on the project's own openapi.yaml.
func minimalSpec(t *testing.T) *openapi3.T {
	t.Helper()
	const yaml = `
openapi: 3.0.3
info:
  title: t
  version: "0"
servers:
  - url: https://will-be-stripped.example
paths:
  /ping:
    get:
      operationId: ping
      security:
        - apiKey: []
      parameters:
        - in: query
          name: q
          required: true
          schema:
            type: string
            minLength: 1
      responses:
        "200":
          description: ok
components:
  securitySchemes:
    apiKey:
      type: apiKey
      in: header
      name: X-API-Key
`
	loader := openapi3.NewLoader()
	spec, err := loader.LoadFromData([]byte(yaml))
	if err != nil {
		t.Fatalf("load minimal spec: %v", err)
	}
	if err := spec.Validate(loader.Context); err != nil {
		t.Fatalf("spec invalid: %v", err)
	}
	return spec
}

func TestOpenAPIValidator_ValidRequestPasses(t *testing.T) {
	spec := minimalSpec(t)
	called := false
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	})

	req := httptest.NewRequest(http.MethodGet, "/ping?q=hi", nil)
	rec := httptest.NewRecorder()
	OpenAPIValidator(spec)(next).ServeHTTP(rec, req)

	if !called {
		t.Fatal("downstream not reached for valid request")
	}
	if rec.Code != http.StatusOK {
		t.Errorf("status = %d; want 200", rec.Code)
	}
}

func TestOpenAPIValidator_MissingRequiredQueryParamRejected(t *testing.T) {
	spec := minimalSpec(t)
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("downstream must not be called for invalid request")
	})

	req := httptest.NewRequest(http.MethodGet, "/ping", nil)
	rec := httptest.NewRecorder()
	OpenAPIValidator(spec)(next).ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d; want 400 (missing required query param)", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("Content-Type = %q; want application/json", ct)
	}
	if !strings.Contains(rec.Body.String(), `"error":`) {
		t.Errorf("body %q must be JSON with an error field", rec.Body.String())
	}
}

func TestOpenAPIValidator_AuthBypassed_NoAPIKeyHeaderStillAllowed(t *testing.T) {
	// AuthenticationFunc is intentionally a no-op so kin-openapi doesn't
	// gate on the apiKey scheme — middleware.Auth owns that. Regression for
	// the bug class where adding apiKey to the spec broke every request.
	spec := minimalSpec(t)
	called := false
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	})

	req := httptest.NewRequest(http.MethodGet, "/ping?q=hi", nil)
	rec := httptest.NewRecorder()
	OpenAPIValidator(spec)(next).ServeHTTP(rec, req)

	if !called {
		t.Fatal("downstream not reached; apiKey requirement is bypassed by AuthenticationFunc")
	}
}

func TestOpenAPIValidator_StripsServersField(t *testing.T) {
	// kin-openapi rejects requests whose Host doesn't match a declared server.
	// CALM strips spec.Servers so the validator allows any host.
	spec := minimalSpec(t)
	OpenAPIValidator(spec)(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	if spec.Servers != nil {
		t.Errorf("spec.Servers = %+v after OpenAPIValidator; want nil", spec.Servers)
	}
}
