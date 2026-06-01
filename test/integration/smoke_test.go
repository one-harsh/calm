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

	assert.Equal(t, http.StatusNotImplemented, resp.StatusCode,
		"handler is stubbed today; route resolution + chain reach handler is the proof")
	assert.Equal(t, "application/json", resp.Header.Get("Content-Type"))
}

func TestHarness_RequestIDPropagation(t *testing.T) {
	t.Parallel()

	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, env.serverURL+"/v1/sources", nil)
	require.NoError(t, err)
	req.Header.Set(auth.HeaderAPIKey, testMasterKey)
	req.Header.Set("X-CALM-Session-Token", uniqueSessionToken(t))

	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	assert.NotEmpty(t, resp.Header.Get("X-Request-Id"),
		"context middleware must stamp a request id on every response")
}

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
