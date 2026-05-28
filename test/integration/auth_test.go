// Copyright 2026 The CALM Authors
// SPDX-License-Identifier: Apache-2.0

package integration

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/one-harsh/calm/internal/api/genapi"
)

func TestBearerAuthSucceeds(t *testing.T) {
	resp, err := env.client.CreateSessionWithResponse(context.Background(), genapi.CreateSessionJSONRequestBody{
		SessionId: "auth-smoke",
	})
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	if resp.StatusCode() != http.StatusCreated {
		t.Fatalf("status = %d; want 201", resp.StatusCode())
	}
}

func TestMissingAuthHeaderRejected(t *testing.T) {
	status := rawPOSTStatus(t, "/v1/sessions", `{"session_id":"x"}`, nil)
	if status != http.StatusUnauthorized {
		t.Errorf("status = %d; want 401", status)
	}
}

func TestNonBearerSchemeRejected(t *testing.T) {
	status := rawPOSTStatus(t, "/v1/sessions", `{"session_id":"x"}`, map[string]string{
		"Authorization": "Basic dXNlcjpwYXNz",
	})
	if status != http.StatusUnauthorized {
		t.Errorf("status = %d; want 401", status)
	}
}

func TestUnknownBearerKeyRejected(t *testing.T) {
	status := rawPOSTStatus(t, "/v1/sessions", `{"session_id":"x"}`, map[string]string{
		"Authorization": "Bearer not-a-real-key",
	})
	if status != http.StatusUnauthorized {
		t.Errorf("status = %d; want 401", status)
	}
}

// rawPOSTStatus posts a body to the test server's path with the supplied
// extra headers and returns the response status code. Used by the auth
// negative-path tests to bypass the bearer-equipped genapi client.
func rawPOSTStatus(t *testing.T, path, body string, extraHeaders map[string]string) int {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, env.serverURL+path, strings.NewReader(body))
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	for k, v := range extraHeaders {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("do request: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	_, _ = io.Copy(io.Discard, resp.Body)
	return resp.StatusCode
}
