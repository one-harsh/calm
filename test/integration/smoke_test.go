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
)

func TestHarness_RoutesResolve(t *testing.T) {
	t.Parallel()

	resp, err := http.Get(env.serverURL + "/v1/sessions/" + uniqueSessionID(t) + "/sources")
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	assert.Equal(t, http.StatusNotImplemented, resp.StatusCode,
		"handler is stubbed today; route resolution + chain reach handler is the proof")
	assert.Equal(t, "application/json", resp.Header.Get("Content-Type"))
}

func TestHarness_RequestIDPropagation(t *testing.T) {
	t.Parallel()

	resp, err := http.Get(env.serverURL + "/v1/sessions/" + uniqueSessionID(t) + "/sources")
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	assert.NotEmpty(t, resp.Header.Get("X-Request-Id"),
		"context middleware must stamp a request id on every response")
}

func TestHarness_OpenAPIValidationFiresBeforeHandler(t *testing.T) {
	t.Parallel()

	// Body missing the required `session_id` field. Validation middleware
	// should reject with 400 before the handler ever sees the request.
	body := bytes.NewBufferString(`{}`)
	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, env.serverURL+"/v1/sessions", body)
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	assert.Equal(t, http.StatusBadRequest, resp.StatusCode,
		"openapi validator must reject malformed body before the handler returns 501")
}
