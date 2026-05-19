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

// TestBearerAuthSucceeds proves the harness's master key clears the auth
// middleware. CreateSession is still a 501 stub at WI-01 time, so asserting
// exactly 501 means this test fails loudly when WI-07 wires the real handler
// — that failure is a useful prompt to reframe this assertion (e.g., switch
// to expecting 201) rather than letting the test pass coincidentally.
func TestBearerAuthSucceeds(t *testing.T) {
	resp, err := env.client.CreateSessionWithResponse(context.Background(), genapi.CreateSessionJSONRequestBody{
		SessionId: "wi-01-auth-smoke",
	})
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	if resp.StatusCode() != http.StatusNotImplemented {
		t.Fatalf("status = %d; want 501 (stub). If WI-07 has landed, reframe this test for the new CreateSession shape.", resp.StatusCode())
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
