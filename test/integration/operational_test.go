// Copyright 2026 The CALM Authors
// SPDX-License-Identifier: Apache-2.0

package integration

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/one-harsh/calm/internal/api/genapi"
)

// GET /v1/health returns ok with a passing postgres check when the DB is reachable.
func TestHealthReportsPostgresReachable(t *testing.T) {
	t.Parallel()

	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, env.serverURL+"/v1/health", nil)
	require.NoError(t, err)

	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	require.Equal(t, http.StatusOK, resp.StatusCode)

	var body genapi.HealthResult
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&body))
	assert.Equal(t, genapi.HealthResultStatusOk, body.Status)
	assert.Equal(t, genapi.Ok, body.Checks["postgres"])
}

// GET /v1/version reports the version string the service was configured with.
func TestVersionReportsConfiguredVersion(t *testing.T) {
	t.Parallel()

	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, env.serverURL+"/v1/version", nil)
	require.NoError(t, err)

	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	require.Equal(t, http.StatusOK, resp.StatusCode)

	var body genapi.VersionResult
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&body))
	assert.Equal(t, testVersion, body.Version)
}
