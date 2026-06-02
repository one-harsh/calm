// Copyright 2026 The CALM Authors
// SPDX-License-Identifier: Apache-2.0

package integration

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	logging "github.com/one-harsh/context-logging"

	"github.com/one-harsh/calm/internal/api/genapi"
	"github.com/one-harsh/calm/internal/api/handlers"
	"github.com/one-harsh/calm/internal/auth"
	"github.com/one-harsh/calm/internal/clientreg"
	"github.com/one-harsh/calm/internal/server"
	"github.com/one-harsh/calm/internal/session"
)

// ratelimitTestCfg controls the rate-limit tiers for a per-test server.
// Zero values disable the corresponding tier (matches production semantics).
type ratelimitTestCfg struct {
	NSDefault         int
	GlobalCap         int
	PerIP             int
	TrustProxyHeaders bool
	NSOverrides       map[string]int
}

// newRateLimitTestServer builds a fresh handler + httptest server with custom
// rate-limit config, isolated from the package's shared `env`. Reuses the
// suite's test Postgres (`env.store`) and the standard test keys. Returns
// clients for default and tenant-a namespaces, the raw URL, and a teardown.
func newRateLimitTestServer(t *testing.T, cfg ratelimitTestCfg) (defaultClient, tenantAClient *genapi.ClientWithResponses, serverURL string, teardown func()) {
	t.Helper()

	registry := auth.NewMemoryRegistry(
		map[string]string{
			testMasterKey:  testNamespace,
			testTenantAKey: testTenantANamespace,
		},
		cfg.NSOverrides,
		nil,
	)

	sessionSvc := session.New(env.store, session.Config{CacheSize: 10_000})
	handler, err := server.NewHandler(server.Config{
		MaxIngestPayloadKB:       1024,
		RateLimitPerSecond:       cfg.NSDefault,
		RateLimitGlobalPerSecond: cfg.GlobalCap,
		RateLimitPerIPPerSecond:  cfg.PerIP,
		TrustProxyHeaders:        cfg.TrustProxyHeaders,
		RequestTimeout:           2 * time.Second,
		GracefulShutdownWait:     0,
	}, server.Deps{
		Logger:   logging.Nop(),
		Registry: registry,
		Sessions: sessionSvc,
		Handlers: handlers.New(handlers.Deps{
			Logger:   logging.Nop(),
			Clients:  clientreg.New(env.store, logging.Nop()),
			Sessions: sessionSvc,
			Events:   env.store.Events(),
			Cfg: handlers.HandlersConfig{
				DefaultTTLMinutes: testDefaultTTLMinutes,
				MaxTTLMinutes:     testMaxTTLMinutes,
			},
		}),
	})
	if err != nil {
		t.Fatalf("build handler: %v", err)
	}
	srv := httptest.NewServer(handler)

	mkClient := func(key string) *genapi.ClientWithResponses {
		c, err := genapi.NewClientWithResponses(srv.URL, genapi.WithRequestEditorFn(apiKeyHeader(key)))
		if err != nil {
			t.Fatalf("build client: %v", err)
		}
		return c
	}
	return mkClient(testMasterKey), mkClient(testTenantAKey), srv.URL, srv.Close
}

func TestRateLimit_NamespaceHammeredReturns429AfterBurst(t *testing.T) {
	def, _, _, teardown := newRateLimitTestServer(t, ratelimitTestCfg{NSDefault: 5, PerIP: 1000})
	defer teardown()

	// Burst = 2 × 5 = 10. First 10 return 404 (handler reached); 11th → 429.
	for i := 0; i < 10; i++ {
		resp, err := def.DeleteSessionWithResponse(context.Background(), &genapi.DeleteSessionParams{XCALMSessionToken: "never-existed"})
		if err != nil {
			t.Fatalf("req %d: %v", i+1, err)
		}
		if resp.StatusCode() != http.StatusNotFound {
			t.Fatalf("req %d: status=%d; want 404", i+1, resp.StatusCode())
		}
	}
	resp, err := def.DeleteSessionWithResponse(context.Background(), &genapi.DeleteSessionParams{XCALMSessionToken: "never-existed"})
	if err != nil {
		t.Fatalf("11th: %v", err)
	}
	if resp.StatusCode() != http.StatusTooManyRequests {
		t.Fatalf("11th: status=%d; want 429; body=%s", resp.StatusCode(), string(resp.Body))
	}
	if got := resp.HTTPResponse.Header.Get("Retry-After"); got != "1" {
		t.Errorf("Retry-After=%q; want \"1\"", got)
	}
	body := decode429Body(t, resp.Body)
	if body["error"] != "rate_limited" {
		t.Errorf("body.error=%v; want rate_limited", body["error"])
	}
	if body["endpoint"] != "/v1/sessions" {
		t.Errorf("body.endpoint=%v; want /v1/sessions", body["endpoint"])
	}
	if detail, _ := body["detail"].(string); !strings.Contains(detail, "namespace") {
		t.Errorf("body.detail=%q; want substring \"namespace\"", detail)
	}
}

func TestRateLimit_OtherNamespaceUnaffected(t *testing.T) {
	def, tenA, _, teardown := newRateLimitTestServer(t, ratelimitTestCfg{NSDefault: 5, PerIP: 1000})
	defer teardown()

	// Exhaust default's burst (10) + one 429.
	for i := 0; i < 10; i++ {
		_, _ = def.DeleteSessionWithResponse(context.Background(), &genapi.DeleteSessionParams{XCALMSessionToken: "never-existed"})
	}
	if resp, err := def.DeleteSessionWithResponse(context.Background(), &genapi.DeleteSessionParams{XCALMSessionToken: "never-existed"}); err != nil || resp.StatusCode() != http.StatusTooManyRequests {
		t.Fatalf("default should be throttled: status=%d err=%v", resp.StatusCode(), err)
	}

	// tenant-a's first request must reach the handler.
	resp, err := tenA.DeleteSessionWithResponse(context.Background(), &genapi.DeleteSessionParams{XCALMSessionToken: "never-existed"})
	if err != nil {
		t.Fatalf("tenant-a: %v", err)
	}
	if resp.StatusCode() != http.StatusNotFound {
		t.Errorf("tenant-a: status=%d; want 404 (handler reached)", resp.StatusCode())
	}
}

func TestRateLimit_PerNamespaceOverrideHonored(t *testing.T) {
	def, tenA, _, teardown := newRateLimitTestServer(t, ratelimitTestCfg{
		NSDefault:   100,
		PerIP:       1000,
		NSOverrides: map[string]int{testTenantANamespace: 2}, // burst = 4
	})
	defer teardown()

	for i := 0; i < 4; i++ {
		resp, err := tenA.DeleteSessionWithResponse(context.Background(), &genapi.DeleteSessionParams{XCALMSessionToken: "never-existed"})
		if err != nil || resp.StatusCode() != http.StatusNotFound {
			t.Fatalf("tenant-a req %d: status=%d err=%v", i+1, resp.StatusCode(), err)
		}
	}
	resp, err := tenA.DeleteSessionWithResponse(context.Background(), &genapi.DeleteSessionParams{XCALMSessionToken: "never-existed"})
	if err != nil {
		t.Fatalf("tenant-a 5th: %v", err)
	}
	if resp.StatusCode() != http.StatusTooManyRequests {
		t.Fatalf("tenant-a 5th: status=%d; want 429", resp.StatusCode())
	}

	// default unaffected by the per-NS override.
	respD, err := def.DeleteSessionWithResponse(context.Background(), &genapi.DeleteSessionParams{XCALMSessionToken: "never-existed"})
	if err != nil {
		t.Fatalf("default: %v", err)
	}
	if respD.StatusCode() != http.StatusNotFound {
		t.Errorf("default: status=%d; want 404 (override should not leak)", respD.StatusCode())
	}
}

func TestRateLimit_GlobalCapTrips(t *testing.T) {
	def, _, _, teardown := newRateLimitTestServer(t, ratelimitTestCfg{
		NSDefault: 1000, // out of the way
		GlobalCap: 3,    // burst = 6
		PerIP:     1000, // out of the way
	})
	defer teardown()

	for i := 0; i < 6; i++ {
		resp, err := def.DeleteSessionWithResponse(context.Background(), &genapi.DeleteSessionParams{XCALMSessionToken: "never-existed"})
		if err != nil || resp.StatusCode() != http.StatusNotFound {
			t.Fatalf("req %d: status=%d err=%v", i+1, resp.StatusCode(), err)
		}
	}
	resp, err := def.DeleteSessionWithResponse(context.Background(), &genapi.DeleteSessionParams{XCALMSessionToken: "never-existed"})
	if err != nil {
		t.Fatalf("7th: %v", err)
	}
	if resp.StatusCode() != http.StatusTooManyRequests {
		t.Fatalf("7th: status=%d; want 429", resp.StatusCode())
	}
	body := decode429Body(t, resp.Body)
	if detail, _ := body["detail"].(string); !strings.Contains(detail, "global") {
		t.Errorf("body.detail=%q; want substring \"global\"", detail)
	}
}

func TestRateLimit_PerIPCapTrips(t *testing.T) {
	def, _, _, teardown := newRateLimitTestServer(t, ratelimitTestCfg{
		NSDefault: 1000,
		PerIP:     3, // burst = 6
	})
	defer teardown()

	for i := 0; i < 6; i++ {
		resp, err := def.DeleteSessionWithResponse(context.Background(), &genapi.DeleteSessionParams{XCALMSessionToken: "never-existed"})
		if err != nil || resp.StatusCode() != http.StatusNotFound {
			t.Fatalf("req %d: status=%d err=%v", i+1, resp.StatusCode(), err)
		}
	}
	resp, err := def.DeleteSessionWithResponse(context.Background(), &genapi.DeleteSessionParams{XCALMSessionToken: "never-existed"})
	if err != nil {
		t.Fatalf("7th: %v", err)
	}
	if resp.StatusCode() != http.StatusTooManyRequests {
		t.Fatalf("7th: status=%d; want 429", resp.StatusCode())
	}
	body := decode429Body(t, resp.Body)
	if detail, _ := body["detail"].(string); !strings.Contains(detail, "per-ip") {
		t.Errorf("body.detail=%q; want substring \"per-ip\"", detail)
	}
}

func TestRateLimit_PerIPTrumpsAuthFailure(t *testing.T) {
	// DDoS-defense contract: IP tier sits before Auth, so once an IP's burst
	// is exhausted, even unauthenticated requests get 429 (not 401).
	_, _, serverURL, teardown := newRateLimitTestServer(t, ratelimitTestCfg{
		NSDefault: 1000,
		PerIP:     3, // burst = 6
	})
	defer teardown()

	mkInvalidReq := func() *http.Request {
		req, err := http.NewRequest(http.MethodDelete, serverURL+"/v1/sessions", nil)
		if err != nil {
			t.Fatalf("build req: %v", err)
		}
		req.Header.Set(auth.HeaderAPIKey, "invalid-bogus-key")
		return req
	}

	// First 6 requests: IP tier passes (within burst), Auth rejects → 401.
	for i := 0; i < 6; i++ {
		resp, err := http.DefaultClient.Do(mkInvalidReq())
		if err != nil {
			t.Fatalf("req %d: %v", i+1, err)
		}
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusUnauthorized {
			t.Fatalf("req %d: status=%d; want 401 (auth runs after IP tier passes)", i+1, resp.StatusCode)
		}
	}

	// 7th: IP tier 429s before Auth runs.
	resp, err := http.DefaultClient.Do(mkInvalidReq())
	if err != nil {
		t.Fatalf("7th: %v", err)
	}
	bodyBytes, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("7th: status=%d; want 429 (IP tier should fire); body=%s", resp.StatusCode, string(bodyBytes))
	}
	body := decode429Body(t, bodyBytes)
	if detail, _ := body["detail"].(string); !strings.Contains(detail, "per-ip") {
		t.Errorf("body.detail=%q; want substring \"per-ip\"", detail)
	}
}

func TestRateLimit_RecoversAfterCooldown(t *testing.T) {
	def, _, _, teardown := newRateLimitTestServer(t, ratelimitTestCfg{NSDefault: 5, PerIP: 1000})
	defer teardown()

	// Exhaust burst.
	for i := 0; i < 10; i++ {
		_, _ = def.DeleteSessionWithResponse(context.Background(), &genapi.DeleteSessionParams{XCALMSessionToken: "never-existed"})
	}
	resp, err := def.DeleteSessionWithResponse(context.Background(), &genapi.DeleteSessionParams{XCALMSessionToken: "never-existed"})
	if err != nil {
		t.Fatalf("post-burst: %v", err)
	}
	if resp.StatusCode() != http.StatusTooManyRequests {
		t.Fatalf("post-burst: status=%d; want 429", resp.StatusCode())
	}

	// Wait just over one token's refill interval (5/sec → 200ms per token).
	time.Sleep(1200 * time.Millisecond)

	resp, err = def.DeleteSessionWithResponse(context.Background(), &genapi.DeleteSessionParams{XCALMSessionToken: "never-existed"})
	if err != nil {
		t.Fatalf("post-cooldown: %v", err)
	}
	if resp.StatusCode() != http.StatusNotFound {
		t.Errorf("post-cooldown: status=%d; want 404 (recovered)", resp.StatusCode())
	}
}

// decode429Body parses the JSON body of a 429 response into a map. Fatal on
// parse failure (so the caller doesn't have to bother with error handling).
func decode429Body(t *testing.T, body []byte) map[string]any {
	t.Helper()
	var got map[string]any
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatalf("decode 429 body: %v; raw=%s", err, string(body))
	}
	return got
}
