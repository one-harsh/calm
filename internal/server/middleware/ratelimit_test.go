// Copyright 2026 The CALM Authors
// SPDX-License-Identifier: Apache-2.0

package middleware

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	logging "github.com/one-harsh/context-logging"

	"github.com/one-harsh/calm/internal/auth"
)

// okHandler always responds 200 and signals via a captured bool that it ran.
func okHandler(called *bool) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		*called = true
		w.WriteHeader(http.StatusOK)
	})
}

func newRequest(t *testing.T, ip, ns string) *http.Request {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/x", nil)
	req.RemoteAddr = ip
	if ns != "" {
		req = req.WithContext(auth.WithNamespace(req.Context(), ns))
	}
	return req
}

func decode429(t *testing.T, body []byte) map[string]any {
	t.Helper()
	var got map[string]any
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatalf("decode 429 body: %v; raw=%s", err, string(body))
	}
	return got
}

// ----- RateLimitIP -----

func TestRateLimitIP_DisabledWhenZero(t *testing.T) {
	for _, perSec := range []int{0, -1} {
		called := false
		mw := RateLimitIP(perSec, false, logging.Nop())
		rec := httptest.NewRecorder()
		mw(okHandler(&called)).ServeHTTP(rec, newRequest(t, "1.2.3.4:1", ""))
		if !called || rec.Code != http.StatusOK {
			t.Errorf("perSec=%d: want passthrough; got called=%v status=%d", perSec, called, rec.Code)
		}
	}
}

func TestRateLimitIP_AllowsUpToBurstThenThrottles(t *testing.T) {
	called := 0
	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		called++
		w.WriteHeader(http.StatusOK)
	})
	mw := RateLimitIP(5, false, logging.Nop())(next)

	// Burst = 2 × 5 = 10. First 10 pass; 11th throttles.
	for i := 0; i < 10; i++ {
		rec := httptest.NewRecorder()
		mw.ServeHTTP(rec, newRequest(t, "1.2.3.4:1", ""))
		if rec.Code != http.StatusOK {
			t.Fatalf("request %d: got %d; want 200", i+1, rec.Code)
		}
	}
	rec := httptest.NewRecorder()
	mw.ServeHTTP(rec, newRequest(t, "1.2.3.4:1", ""))
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("11th request: got %d; want 429", rec.Code)
	}
	if got := rec.Header().Get("Retry-After"); got != "1" {
		t.Errorf("Retry-After = %q; want \"1\"", got)
	}
	body := decode429(t, rec.Body.Bytes())
	if body["error"] != "rate_limited" {
		t.Errorf("body.error = %v; want rate_limited", body["error"])
	}
	if detail, _ := body["detail"].(string); !strings.Contains(detail, "per-ip") {
		t.Errorf("body.detail = %q; want substring \"per-ip\"", detail)
	}
	if called != 10 {
		t.Errorf("handler called %d times; want 10", called)
	}
}

func TestRateLimitIP_PerIPIndependent(t *testing.T) {
	mw := RateLimitIP(2, false, logging.Nop())(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	// Exhaust IP-A's burst (2 × 2 = 4).
	for i := 0; i < 4; i++ {
		rec := httptest.NewRecorder()
		mw.ServeHTTP(rec, newRequest(t, "1.1.1.1:1", ""))
		if rec.Code != http.StatusOK {
			t.Fatalf("ip-A req %d: got %d", i+1, rec.Code)
		}
	}
	recA := httptest.NewRecorder()
	mw.ServeHTTP(recA, newRequest(t, "1.1.1.1:1", ""))
	if recA.Code != http.StatusTooManyRequests {
		t.Fatalf("ip-A 5th: got %d; want 429", recA.Code)
	}

	// IP-B's first request must pass.
	recB := httptest.NewRecorder()
	mw.ServeHTTP(recB, newRequest(t, "2.2.2.2:1", ""))
	if recB.Code != http.StatusOK {
		t.Fatalf("ip-B 1st: got %d; want 200", recB.Code)
	}
}

func TestRateLimitIP_TrustProxyHeadersHonored(t *testing.T) {
	// With trustProxyHeaders=true: each XFF gets its own bucket.
	mwTrusted := RateLimitIP(1, true, logging.Nop())(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	mkReq := func(xff string) *http.Request {
		req := httptest.NewRequest(http.MethodGet, "/x", nil)
		req.RemoteAddr = "10.0.0.1:1" // same edge IP for both
		req.Header.Set("X-Forwarded-For", xff)
		return req
	}
	// Burst = 2 from each XFF. Hit one until throttled, other should still pass.
	for i := 0; i < 2; i++ {
		rec := httptest.NewRecorder()
		mwTrusted.ServeHTTP(rec, mkReq("3.3.3.3"))
		if rec.Code != http.StatusOK {
			t.Fatalf("xff-A req %d: got %d", i+1, rec.Code)
		}
	}
	recA := httptest.NewRecorder()
	mwTrusted.ServeHTTP(recA, mkReq("3.3.3.3"))
	if recA.Code != http.StatusTooManyRequests {
		t.Fatalf("xff-A 3rd: got %d; want 429", recA.Code)
	}
	recB := httptest.NewRecorder()
	mwTrusted.ServeHTTP(recB, mkReq("4.4.4.4"))
	if recB.Code != http.StatusOK {
		t.Fatalf("xff-B 1st: got %d; want 200", recB.Code)
	}

	// With trustProxyHeaders=false: XFF is ignored; both share one bucket.
	mwUntrusted := RateLimitIP(1, false, logging.Nop())(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	for i := 0; i < 2; i++ {
		rec := httptest.NewRecorder()
		mwUntrusted.ServeHTTP(rec, mkReq("5.5.5.5"))
		if rec.Code != http.StatusOK {
			t.Fatalf("untrusted xff req %d: got %d", i+1, rec.Code)
		}
	}
	rec := httptest.NewRecorder()
	mwUntrusted.ServeHTTP(rec, mkReq("6.6.6.6")) // different XFF, same RemoteAddr
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("untrusted different-XFF should share bucket: got %d; want 429", rec.Code)
	}
}

func TestRateLimitIP_XFFMultipleAddressesUsesLeftmost(t *testing.T) {
	mw := RateLimitIP(1, true, logging.Nop())(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	mkReq := func(xff string) *http.Request {
		req := httptest.NewRequest(http.MethodGet, "/x", nil)
		req.RemoteAddr = "10.0.0.1:1"
		req.Header.Set("X-Forwarded-For", xff)
		return req
	}
	// Both should hit the same bucket (leftmost IP = 1.2.3.4).
	for i := 0; i < 2; i++ {
		rec := httptest.NewRecorder()
		mw.ServeHTTP(rec, mkReq("1.2.3.4, 10.0.0.2"))
		if rec.Code != http.StatusOK {
			t.Fatalf("req %d: got %d", i+1, rec.Code)
		}
	}
	rec := httptest.NewRecorder()
	mw.ServeHTTP(rec, mkReq("1.2.3.4, 10.0.0.99")) // leftmost same; different trailing proxy
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("leftmost-IP collapse: got %d; want 429", rec.Code)
	}
}

func TestRateLimitIP_BucketEvictionSweepsIdle(t *testing.T) {
	// Tiny thresholds so the test runs fast: sweep when len > 3, every 2
	// inserts. With these, the 6th insert triggers the first sweep (inserts=6
	// is divisible by 2, and pre-insert size=5 > 3).
	store := newIPBucketStore(1, 3, 2)

	for _, ip := range []string{"a", "b", "c", "d", "e"} {
		store.bucketFor(ip)
	}
	if got := store.size(); got != 5 {
		t.Fatalf("pre-sweep: size=%d; want 5", got)
	}
	// 6th insert: sweep runs (all prior buckets idle → deleted), then "f" is
	// added fresh. Only "f" remains.
	store.bucketFor("f")
	if got := store.size(); got != 1 {
		t.Errorf("post-sweep: size=%d; want 1 (only the new \"f\" bucket)", got)
	}
}

// ----- RateLimitNamespaceAndGlobal -----

func newNSGMiddleware(t *testing.T, defaultNS, global int, overrides map[string]int) (func(http.Handler) http.Handler, func() int) {
	t.Helper()
	calls := 0
	registry := auth.NewMemoryRegistry(map[string]string{"k": "ns-a"}, overrides, nil, nil, nil, nil)
	mw := RateLimitNamespaceAndGlobal(registry, defaultNS, global, logging.Nop())
	return func(next http.Handler) http.Handler {
		return mw(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			calls++
			next.ServeHTTP(w, r)
		}))
	}, func() int { return calls }
}

func TestRateLimitNSG_BothTiersDisabled(t *testing.T) {
	mw, _ := newNSGMiddleware(t, 0, 0, nil)
	rec := httptest.NewRecorder()
	mw(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })).
		ServeHTTP(rec, newRequest(t, "1.1.1.1:1", "ns-a"))
	if rec.Code != http.StatusOK {
		t.Errorf("passthrough: got %d", rec.Code)
	}
}

func TestRateLimitNSG_NamespaceAllowsUpToBurstThenThrottles(t *testing.T) {
	registry := auth.NewMemoryRegistry(map[string]string{"k": "ns-a"}, nil, nil, nil, nil, nil)
	mw := RateLimitNamespaceAndGlobal(registry, 5, 0, logging.Nop())(
		http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) }),
	)
	// Burst = 10.
	for i := 0; i < 10; i++ {
		rec := httptest.NewRecorder()
		mw.ServeHTTP(rec, newRequest(t, "1.1.1.1:1", "ns-a"))
		if rec.Code != http.StatusOK {
			t.Fatalf("req %d: got %d", i+1, rec.Code)
		}
	}
	rec := httptest.NewRecorder()
	mw.ServeHTTP(rec, newRequest(t, "1.1.1.1:1", "ns-a"))
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("11th: got %d; want 429", rec.Code)
	}
	body := decode429(t, rec.Body.Bytes())
	if detail, _ := body["detail"].(string); !strings.Contains(detail, "namespace") {
		t.Errorf("body.detail = %q; want substring \"namespace\"", detail)
	}
}

func TestRateLimitNSG_PerNamespaceIndependent(t *testing.T) {
	registry := auth.NewMemoryRegistry(map[string]string{"a": "ns-a", "b": "ns-b"}, nil, nil, nil, nil, nil)
	mw := RateLimitNamespaceAndGlobal(registry, 1, 0, logging.Nop())(
		http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) }),
	)
	// Exhaust ns-a (burst = 2).
	for i := 0; i < 2; i++ {
		rec := httptest.NewRecorder()
		mw.ServeHTTP(rec, newRequest(t, "1.1.1.1:1", "ns-a"))
		if rec.Code != http.StatusOK {
			t.Fatalf("ns-a req %d: got %d", i+1, rec.Code)
		}
	}
	recA := httptest.NewRecorder()
	mw.ServeHTTP(recA, newRequest(t, "1.1.1.1:1", "ns-a"))
	if recA.Code != http.StatusTooManyRequests {
		t.Fatalf("ns-a 3rd: got %d; want 429", recA.Code)
	}
	// ns-b's first request passes.
	recB := httptest.NewRecorder()
	mw.ServeHTTP(recB, newRequest(t, "1.1.1.1:1", "ns-b"))
	if recB.Code != http.StatusOK {
		t.Fatalf("ns-b 1st: got %d; want 200", recB.Code)
	}
}

func TestRateLimitNSG_HonorsNamespaceOverride(t *testing.T) {
	registry := auth.NewMemoryRegistry(
		map[string]string{"a": "ns-a", "b": "ns-b"},
		map[string]int{"ns-a": 2},
		nil,
		nil,
		nil,
		nil,
	)
	mw := RateLimitNamespaceAndGlobal(registry, 100, 0, logging.Nop())(
		http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) }),
	)
	// ns-a override = 2 → burst = 4.
	for i := 0; i < 4; i++ {
		rec := httptest.NewRecorder()
		mw.ServeHTTP(rec, newRequest(t, "1.1.1.1:1", "ns-a"))
		if rec.Code != http.StatusOK {
			t.Fatalf("ns-a req %d: got %d", i+1, rec.Code)
		}
	}
	rec := httptest.NewRecorder()
	mw.ServeHTTP(rec, newRequest(t, "1.1.1.1:1", "ns-a"))
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("ns-a 5th: got %d; want 429", rec.Code)
	}
	// ns-b unaffected by override.
	recB := httptest.NewRecorder()
	mw.ServeHTTP(recB, newRequest(t, "1.1.1.1:1", "ns-b"))
	if recB.Code != http.StatusOK {
		t.Errorf("ns-b should fall back to default=100: got %d", recB.Code)
	}
}

func TestRateLimitNSG_GlobalCapEnforced(t *testing.T) {
	registry := auth.NewMemoryRegistry(map[string]string{"k": "ns-a"}, nil, nil, nil, nil, nil)
	mw := RateLimitNamespaceAndGlobal(registry, 1000, 3, logging.Nop())(
		http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) }),
	)
	// Global burst = 6.
	for i := 0; i < 6; i++ {
		rec := httptest.NewRecorder()
		mw.ServeHTTP(rec, newRequest(t, "1.1.1.1:1", "ns-a"))
		if rec.Code != http.StatusOK {
			t.Fatalf("req %d: got %d", i+1, rec.Code)
		}
	}
	rec := httptest.NewRecorder()
	mw.ServeHTTP(rec, newRequest(t, "1.1.1.1:1", "ns-a"))
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("7th: got %d; want 429", rec.Code)
	}
	body := decode429(t, rec.Body.Bytes())
	if detail, _ := body["detail"].(string); !strings.Contains(detail, "global") {
		t.Errorf("body.detail = %q; want substring \"global\"", detail)
	}
}

func TestRateLimitNSG_GlobalCapAcrossNamespaces(t *testing.T) {
	registry := auth.NewMemoryRegistry(map[string]string{"a": "ns-a", "b": "ns-b"}, nil, nil, nil, nil, nil)
	mw := RateLimitNamespaceAndGlobal(registry, 1000, 3, logging.Nop())(
		http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) }),
	)
	// Split 6 requests across two namespaces — should still trip the global cap.
	for i := 0; i < 3; i++ {
		rec := httptest.NewRecorder()
		mw.ServeHTTP(rec, newRequest(t, "1.1.1.1:1", "ns-a"))
		if rec.Code != http.StatusOK {
			t.Fatalf("ns-a req %d: got %d", i+1, rec.Code)
		}
	}
	for i := 0; i < 3; i++ {
		rec := httptest.NewRecorder()
		mw.ServeHTTP(rec, newRequest(t, "1.1.1.1:1", "ns-b"))
		if rec.Code != http.StatusOK {
			t.Fatalf("ns-b req %d: got %d", i+1, rec.Code)
		}
	}
	rec := httptest.NewRecorder()
	mw.ServeHTTP(rec, newRequest(t, "1.1.1.1:1", "ns-a"))
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("7th (sum-overload): got %d; want 429", rec.Code)
	}
	body := decode429(t, rec.Body.Bytes())
	if detail, _ := body["detail"].(string); !strings.Contains(detail, "global") {
		t.Errorf("body.detail = %q; want \"global\"", detail)
	}
}

func TestRateLimitNSG_NamespaceCheckedBeforeGlobal(t *testing.T) {
	// Both tiers at the same rate (1/sec, burst=2). After exhausting both,
	// the next request should report "namespace" (checked first), not "global".
	// Namespace-first is load-bearing: it prevents a misbehaving namespace
	// from leaking its overload pressure into the shared global bucket.
	registry := auth.NewMemoryRegistry(map[string]string{"k": "ns-a"}, nil, nil, nil, nil, nil)
	mw := RateLimitNamespaceAndGlobal(registry, 1, 1, logging.Nop())(
		http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) }),
	)
	for i := 0; i < 2; i++ {
		rec := httptest.NewRecorder()
		mw.ServeHTTP(rec, newRequest(t, "1.1.1.1:1", "ns-a"))
		if rec.Code != http.StatusOK {
			t.Fatalf("req %d: got %d", i+1, rec.Code)
		}
	}
	rec := httptest.NewRecorder()
	mw.ServeHTTP(rec, newRequest(t, "1.1.1.1:1", "ns-a"))
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("3rd: got %d; want 429", rec.Code)
	}
	body := decode429(t, rec.Body.Bytes())
	detail, _ := body["detail"].(string)
	if !strings.Contains(detail, "namespace") {
		t.Errorf("body.detail = %q; want substring \"namespace\" (tier order: namespace first)", detail)
	}
	if strings.Contains(detail, "global") {
		t.Errorf("body.detail = %q; should NOT contain \"global\" (namespace checked first)", detail)
	}
}

func TestRateLimitNSG_NamespaceMissingPassesThrough(t *testing.T) {
	// Unauthenticated paths (health, version) bypass auth and have no
	// namespace. NSG must pass them through, not 500 — operational probes
	// are not rate-limited.
	registry := auth.NewMemoryRegistry(map[string]string{"k": "ns-a"}, nil, nil, nil, nil, nil)
	called := false
	mw := RateLimitNamespaceAndGlobal(registry, 100, 0, logging.Nop())(
		http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			called = true
			w.WriteHeader(http.StatusOK)
		}),
	)
	rec := httptest.NewRecorder()
	mw.ServeHTTP(rec, newRequest(t, "1.1.1.1:1", "")) // no namespace
	if !called || rec.Code != http.StatusOK {
		t.Errorf("missing namespace should pass through: called=%v status=%d; want true/200", called, rec.Code)
	}
}

func TestRateLimitNSG_RecoversAfterCooldown(t *testing.T) {
	registry := auth.NewMemoryRegistry(map[string]string{"k": "ns-a"}, nil, nil, nil, nil, nil)
	mw := RateLimitNamespaceAndGlobal(registry, 5, 0, logging.Nop())(
		http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) }),
	)
	for i := 0; i < 10; i++ { // burst
		rec := httptest.NewRecorder()
		mw.ServeHTTP(rec, newRequest(t, "1.1.1.1:1", "ns-a"))
		if rec.Code != http.StatusOK {
			t.Fatalf("burst req %d: got %d", i+1, rec.Code)
		}
	}
	rec := httptest.NewRecorder()
	mw.ServeHTTP(rec, newRequest(t, "1.1.1.1:1", "ns-a"))
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("post-burst: got %d; want 429", rec.Code)
	}
	// Token refill = 1 / rateLim = 1/5s = 200ms; sleep just over one tick.
	time.Sleep(250 * time.Millisecond)
	rec = httptest.NewRecorder()
	mw.ServeHTTP(rec, newRequest(t, "1.1.1.1:1", "ns-a"))
	if rec.Code != http.StatusOK {
		t.Fatalf("after cooldown: got %d; want 200", rec.Code)
	}
}
