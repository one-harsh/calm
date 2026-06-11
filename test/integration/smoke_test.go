// Copyright 2026 The CALM Authors
// SPDX-License-Identifier: Apache-2.0

package integration

import (
	"bytes"
	"context"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/one-harsh/calm/internal/auth"
)

// A real request traverses the full middleware chain and route tree to the live
// handler (200 + JSON) — proves the harness wiring, not just a unit handler.
func TestHarness_RoutesResolve(t *testing.T) {
	t.Parallel()
	s := createSessionForTest(t, testNamespace)

	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, env.serverURL+"/v1/sources", nil)
	require.NoError(t, err)
	req.Header.Set(auth.HeaderAPIKey, testMasterKey)
	req.Header.Set("X-CALM-Session-Token", s.SessionToken)

	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	assert.Equal(t, http.StatusOK, resp.StatusCode,
		"route resolution + middleware chain must reach the live handler")
	assert.Equal(t, "application/json", resp.Header.Get("Content-Type"))
}

// Every response carries a server-minted X-CALM-Correlation-Id, stamped by the
// context middleware even when the workload sends none.
func TestHarness_CorrelationIDPropagation(t *testing.T) {
	t.Parallel()

	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, env.serverURL+"/v1/sources", nil)
	require.NoError(t, err)
	req.Header.Set(auth.HeaderAPIKey, testMasterKey)
	req.Header.Set("X-CALM-Session-Token", uniqueSessionToken(t))

	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	assert.NotEmpty(t, resp.Header.Get("X-CALM-Correlation-Id"),
		"context middleware must stamp a correlation id on every response")
}

// A malformed request body is rejected with 400 by the OpenAPI validator before
// the handler ever runs.
func TestHarness_OpenAPIValidationFiresBeforeHandler(t *testing.T) {
	t.Parallel()

	// Body has a field of the wrong type. Validation middleware should reject
	// with 400 before the handler ever sees the request.
	body := bytes.NewBufferString(`{"ttl_minutes":"not-a-number"}`)
	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, env.serverURL+"/v1/sessions", body)
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(auth.HeaderAPIKey, testMasterKey)

	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	assert.Equal(t, http.StatusBadRequest, resp.StatusCode,
		"openapi validator must reject malformed body before the handler returns 501")
}
