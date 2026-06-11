// Copyright 2026 The CALM Authors
// SPDX-License-Identifier: Apache-2.0

package integration

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/one-harsh/calm/internal/api/genapi"
	"github.com/one-harsh/calm/internal/auth"
	"github.com/one-harsh/calm/internal/db"
)

// A workload creates a session with no optional fields; the server mints a session token, resolves the namespace from the API key, and returns the committed TTL and client.
func TestCreateSessionHandler_HappyMinimal(t *testing.T) {
	resp, err := env.client.CreateSessionWithResponse(
		context.Background(),
		&genapi.CreateSessionParams{},
		genapi.CreateSessionJSONRequestBody{},
	)
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	if resp.StatusCode() != http.StatusCreated {
		t.Fatalf("status = %d (%s); want 201; body=%s", resp.StatusCode(), resp.Status(), string(resp.Body))
	}
	if resp.JSON201 == nil {
		t.Fatal("JSON201 nil on 201 response")
	}
	if resp.JSON201.SessionToken == "" {
		t.Error("session_token in response is empty; server must surface the minted token")
	}
	if resp.JSON201.Namespace != testNamespace {
		t.Errorf("namespace = %q; want %q (resolved from bearer)", resp.JSON201.Namespace, testNamespace)
	}
	if resp.JSON201.Client != db.DefaultClient {
		t.Errorf("client = %q; want %q (default)", resp.JSON201.Client, db.DefaultClient)
	}
	if resp.JSON201.TtlMinutes != testDefaultTTLMinutes {
		t.Errorf("ttl_minutes = %d; want %d (config default)", resp.JSON201.TtlMinutes, testDefaultTTLMinutes)
	}
	if resp.JSON201.CreatedAt.IsZero() {
		t.Error("created_at is zero — must be populated from DB RETURNING")
	}

	// DB row exists under the hash of the minted token.
	hash := auth.HashToken(testNamespace, resp.JSON201.SessionToken)
	n := countRows(t, env.sqlDB,
		`SELECT COUNT(*) FROM sessions WHERE namespace = $1 AND session_token_hash = $2`,
		testNamespace, hash)
	if n != 1 {
		t.Errorf("DB row count = %d; want 1", n)
	}
}

// A workload creates a session with all optional fields set; client, TTL override, and labels are committed and the response echoes the committed shape.
func TestCreateSessionHandler_HappyWithAllFields(t *testing.T) {
	client := "alice"
	seedClient(t, env.sqlDB, testNamespace, client)
	ttl := 60
	labels := map[string]string{"env": "prod", "tier": "1"}
	resp, err := env.client.CreateSessionWithResponse(
		context.Background(),
		&genapi.CreateSessionParams{},
		genapi.CreateSessionJSONRequestBody{
			Client:     &client,
			TtlMinutes: &ttl,
			Labels:     &labels,
		},
	)
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	if resp.StatusCode() != http.StatusCreated {
		t.Fatalf("status = %d; want 201; body=%s", resp.StatusCode(), string(resp.Body))
	}
	if resp.JSON201.Client != "alice" {
		t.Errorf("client = %q; want alice", resp.JSON201.Client)
	}
	if resp.JSON201.TtlMinutes != 60 {
		t.Errorf("ttl_minutes = %d; want 60 (request override)", resp.JSON201.TtlMinutes)
	}
	// Labels are persisted in DB but NOT echoed in response (HLD minimal-shape).
	// Look up the session by hash to recover the surrogate id, then count
	// labels by FK.
	hash := auth.HashToken(testNamespace, resp.JSON201.SessionToken)
	var sessID int64
	if err := env.sqlDB.QueryRowContext(
		context.Background(),
		`SELECT id FROM sessions WHERE namespace = $1 AND session_token_hash = $2`,
		testNamespace, hash,
	).Scan(&sessID); err != nil {
		t.Fatalf("lookup session row: %v", err)
	}
	if labelCount := countRows(t, env.sqlDB,
		`SELECT COUNT(*) FROM session_labels WHERE session_id = $1`,
		sessID); labelCount != 2 {
		t.Errorf("label rows = %d; want 2", labelCount)
	}
}

// Attempting to create a session for a client that has not been registered returns 400 with error=client_not_found; the handler must not implicitly register clients.
func TestCreateSessionHandler_UnregisteredClientReturns400(t *testing.T) {
	// Post-WI-09c: client is a first-class entity. Sessions cannot be created
	// for an unregistered client — the FK violation surfaces as 400
	// client_not_found. (Previously this handler auto-registered the client.)
	client := "freshly-introduced-client"
	resp, err := env.client.CreateSessionWithResponse(
		context.Background(),
		&genapi.CreateSessionParams{},
		genapi.CreateSessionJSONRequestBody{
			Client: &client,
		},
	)
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	if resp.StatusCode() != http.StatusBadRequest {
		t.Fatalf("status = %d; want 400; body=%s", resp.StatusCode(), string(resp.Body))
	}
	if resp.JSON400 == nil {
		t.Fatal("JSON400 nil on 400")
	}
	if resp.JSON400.Error != "client_not_found" {
		t.Errorf("error = %q; want client_not_found", resp.JSON400.Error)
	}
	n := countRows(t, env.sqlDB,
		`SELECT COUNT(*) FROM clients WHERE namespace = $1 AND name = $2`,
		testNamespace, client)
	if n != 0 {
		t.Errorf("client row count = %d; want 0 (handler must not implicitly register)", n)
	}
}

// Omitting the client field defaults to the built-in default client; no explicit registration is required.
func TestCreateSessionHandler_DefaultsEmptyClient(t *testing.T) {
	resp, err := env.client.CreateSessionWithResponse(
		context.Background(),
		&genapi.CreateSessionParams{},
		genapi.CreateSessionJSONRequestBody{},
	)
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	if resp.StatusCode() != http.StatusCreated {
		t.Fatalf("status = %d; want 201; body=%s", resp.StatusCode(), string(resp.Body))
	}
	if resp.JSON201.Client != db.DefaultClient {
		t.Errorf("client = %q; want %q (default fallback)", resp.JSON201.Client, db.DefaultClient)
	}
}

// Omitting ttl_minutes uses the operator-configured default; the response echoes the committed value.
func TestCreateSessionHandler_AbsentTTLUsesConfigInactivityValue(t *testing.T) {
	resp, err := env.client.CreateSessionWithResponse(
		context.Background(),
		&genapi.CreateSessionParams{},
		genapi.CreateSessionJSONRequestBody{},
	)
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	if resp.JSON201.TtlMinutes != testDefaultTTLMinutes {
		t.Errorf("ttl_minutes = %d; want %d (config DefaultTTLMinutes fallback)",
			resp.JSON201.TtlMinutes, testDefaultTTLMinutes)
	}
}

// A session-create request without X-CALM-API-Key returns 401; auth is enforced before any session logic runs.
func TestCreateSessionHandler_MissingBearerReturns401(t *testing.T) {
	// Raw HTTP — bypassing the typed client (which always attaches the bearer).
	httpReq, _ := http.NewRequestWithContext(context.Background(), http.MethodPost,
		env.serverURL+"/v1/sessions",
		bytes.NewBufferString(`{}`))
	httpReq.Header.Set("Content-Type", "application/json")
	httpResp, err := http.DefaultClient.Do(httpReq)
	if err != nil {
		t.Fatalf("http: %v", err)
	}
	defer func() { _ = httpResp.Body.Close() }()
	if httpResp.StatusCode != http.StatusUnauthorized {
		t.Errorf("status = %d; want 401", httpResp.StatusCode)
	}
}

// A requested TTL between the operator max and the OpenAPI cap is clamped to the operator max; the response echoes the committed (clamped) value.
func TestCreateSessionHandler_RequestedTTLAboveOperatorMaxClamped(t *testing.T) {
	// testMaxTTLMinutes is 240; OpenAPI cap is 10080. A value between them
	// passes the validator and gets clamped by the handler. Response echoes
	// the committed (clamped) value.
	requested := 5000
	resp, err := env.client.CreateSessionWithResponse(
		context.Background(),
		&genapi.CreateSessionParams{},
		genapi.CreateSessionJSONRequestBody{
			TtlMinutes: &requested,
		},
	)
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	if resp.StatusCode() != http.StatusCreated {
		t.Fatalf("status = %d; want 201", resp.StatusCode())
	}
	if resp.JSON201.TtlMinutes != testMaxTTLMinutes {
		t.Errorf("ttl_minutes = %d; want %d (operator-max clamp)", resp.JSON201.TtlMinutes, testMaxTTLMinutes)
	}
}

// A ttl_minutes value above the OpenAPI maximum is rejected by the validator with 400 before reaching handler logic.
func TestCreateSessionHandler_OverMaxTTLRejected400(t *testing.T) {
	overMax := 20_000 // > OpenAPI max of 10080
	resp, err := env.client.CreateSessionWithResponse(
		context.Background(),
		&genapi.CreateSessionParams{},
		genapi.CreateSessionJSONRequestBody{
			TtlMinutes: &overMax,
		},
	)
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	if resp.StatusCode() != http.StatusBadRequest {
		t.Fatalf("status = %d; want 400 (OpenAPI validator rejects ttl_minutes > 10080); body=%s",
			resp.StatusCode(), string(resp.Body))
	}
}

// Session-create response never includes a labels field; the minimal Session shape omits labels even when labels were submitted.
func TestCreateSessionHandler_ResponseHasNoLabelsField(t *testing.T) {
	labels := map[string]string{"x": "y"}
	resp, err := env.client.CreateSessionWithResponse(
		context.Background(),
		&genapi.CreateSessionParams{},
		genapi.CreateSessionJSONRequestBody{
			Labels: &labels,
		},
	)
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	if resp.StatusCode() != http.StatusCreated {
		t.Fatalf("status = %d; want 201", resp.StatusCode())
	}
	var raw map[string]any
	if err := json.Unmarshal(resp.Body, &raw); err != nil {
		t.Fatalf("decode raw body: %v", err)
	}
	if _, present := raw["labels"]; present {
		t.Errorf("response body has labels field; HLD says minimal Session shape (no labels): %s", string(resp.Body))
	}
}

// Routes that exist in the OpenAPI spec but have no handler implementation return 501 with error=not_implemented.
func TestNotImplementedHandlersStillReturn501(t *testing.T) {
	httpReq, _ := http.NewRequestWithContext(context.Background(), http.MethodGet,
		env.serverURL+"/v1/manage/sessions", http.NoBody)
	httpReq.Header.Set(auth.HeaderAPIKey, testMasterKey)
	httpResp, err := http.DefaultClient.Do(httpReq)
	if err != nil {
		t.Fatalf("http: %v", err)
	}
	defer func() { _ = httpResp.Body.Close() }()
	if httpResp.StatusCode != http.StatusNotImplemented {
		t.Errorf("status = %d; want 501", httpResp.StatusCode)
	}
	var body map[string]any
	if err := json.NewDecoder(httpResp.Body).Decode(&body); err != nil {
		t.Fatalf("decode 501 body: %v", err)
	}
	if body["error"] != "not_implemented" {
		t.Errorf("error = %v; want not_implemented", body["error"])
	}
}

// A workload deletes its session; the session row and all dependent rows (labels, sources, chunks, events) are removed in cascade, and the client's last_activity_at is stamped.
func TestDeleteSessionHandler_HappyDeletesSessionWithCascade(t *testing.T) {
	seedClient(t, env.sqlDB, testNamespace, db.DefaultClient)
	s := seedSession(t, env.sqlDB, testNamespace, db.DefaultClient, 60)
	seedSessionLabel(t, env.sqlDB, s.ID, "env", "prod")
	seedSessionLabel(t, env.sqlDB, s.ID, "tier", "1")
	src := seedSource(t, env.sqlDB, s.ID, "spec.md")
	seedChunk(t, env.sqlDB, src, "Section 1", "body", "prose")
	seedChunk(t, env.sqlDB, src, "Section 2", "body", "prose")
	seedEvent(t, env.sqlDB, s.ID, "ingest", 1, []byte(`{"x":1}`))

	resp, err := env.client.DeleteSessionWithResponse(context.Background(),
		&genapi.DeleteSessionParams{XCALMSessionToken: s.SessionToken})
	if err != nil {
		t.Fatalf("DeleteSession: %v", err)
	}
	if resp.StatusCode() != http.StatusNoContent {
		t.Fatalf("status = %d; want 204; body=%s", resp.StatusCode(), string(resp.Body))
	}
	if len(resp.Body) != 0 {
		t.Errorf("204 response carried %d bytes of body; want empty", len(resp.Body))
	}

	if n := countRows(
		t, env.sqlDB,
		`SELECT COUNT(*) FROM sessions WHERE id = $1`, s.ID,
	); n != 0 {
		t.Errorf("sessions row count after delete = %d; want 0", n)
	}
	for _, tbl := range []string{"session_labels", "sources", "session_events"} {
		if n := countRows(
			t, env.sqlDB,
			"SELECT COUNT(*) FROM "+tbl+" WHERE session_id = $1", s.ID,
		); n != 0 {
			t.Errorf("DB row count in %s = %d; want 0 after delete", tbl, n)
		}
	}
	if n := countRows(
		t, env.sqlDB,
		`SELECT COUNT(*) FROM chunks WHERE source_id = $1`, src,
	); n != 0 {
		t.Errorf("chunks row count after delete = %d; want 0 (cascade through sources)", n)
	}

	// HLD explicit-close requirement: clients.last_activity_at bumped so
	// post-teardown observability sees the client's most-recent activity.
	if n := countRows(
		t, env.sqlDB,
		`SELECT COUNT(*) FROM clients WHERE namespace = $1 AND name = $2 AND last_activity_at IS NOT NULL`,
		testNamespace, db.DefaultClient,
	); n != 1 {
		t.Errorf("clients.last_activity_at not set after explicit close: matching-rows = %d; want 1", n)
	}
}

// An explicit workload DELETE bumps clients.last_activity_at to the current time, not the session's last_activity; the scanner-driven path behaves differently.
//
// Regression: explicit DELETE bumps clients.last_activity_at to NOW(); the
// scanner-driven DeleteByID path preserves session.last_activity instead.
func TestDeleteSessionHandler_BumpsClientActivityToNow(t *testing.T) {
	seedClient(t, env.sqlDB, testNamespace, db.DefaultClient)
	backdated := time.Now().UTC().Add(-2 * time.Hour)
	s := seedSessionWithActivity(t, env.sqlDB, testNamespace, db.DefaultClient, 240, backdated)

	if _, err := env.sqlDB.ExecContext(
		context.Background(),
		`UPDATE clients SET last_activity_at = $1 WHERE namespace = $2 AND name = $3`,
		backdated, testNamespace, db.DefaultClient,
	); err != nil {
		t.Fatalf("backdate clients.last_activity_at: %v", err)
	}

	resp, err := env.client.DeleteSessionWithResponse(context.Background(),
		&genapi.DeleteSessionParams{XCALMSessionToken: s.SessionToken})
	if err != nil || resp.StatusCode() != http.StatusNoContent {
		t.Fatalf("DeleteSession: err=%v status=%d", err, resp.StatusCode())
	}

	var after time.Time
	if err := env.sqlDB.QueryRowContext(
		context.Background(),
		`SELECT last_activity_at FROM clients WHERE namespace = $1 AND name = $2`,
		testNamespace, db.DefaultClient,
	).Scan(&after); err != nil {
		t.Fatalf("read post-delete last_activity_at: %v", err)
	}
	if !after.After(backdated.Add(time.Hour)) {
		t.Errorf("clients.last_activity_at = %v; want a recent NOW()-style bump (backdated was %v)", after, backdated)
	}
}

// Deleting a session that does not exist returns 404 with error=session_not_found.
func TestDeleteSessionHandler_UnknownSessionReturns404(t *testing.T) {
	resp, err := env.client.DeleteSessionWithResponse(context.Background(),
		&genapi.DeleteSessionParams{XCALMSessionToken: "never-existed-token"})
	if err != nil {
		t.Fatalf("DeleteSession: %v", err)
	}
	if resp.StatusCode() != http.StatusNotFound {
		t.Fatalf("status = %d; want 404; body=%s", resp.StatusCode(), string(resp.Body))
	}
	if resp.JSON404 == nil {
		t.Fatal("JSON404 nil on 404")
	}
	if resp.JSON404.Error != "session_not_found" {
		t.Errorf("error = %q; want session_not_found", resp.JSON404.Error)
	}
	if resp.JSON404.Detail == nil || !strings.Contains(*resp.JSON404.Detail, "session") {
		t.Errorf("detail %v should mention session", resp.JSON404.Detail)
	}
}

// A workload in namespace B holding namespace A's session token cannot delete the session; the lookup misses and returns 404 (namespace-isolation: invisibility, not denial).
func TestDeleteSessionHandler_CrossNamespaceReturns404(t *testing.T) {
	seedClient(t, env.sqlDB, testNamespace, db.DefaultClient)
	s := seedSession(t, env.sqlDB, testNamespace, db.DefaultClient, 60)

	// ns-B holds ns-A's raw token (e.g. leaked / guessed). The token-hash is
	// namespace-scoped, so the lookup misses and returns 404 — invisibility,
	// not denial.
	clientB := env.clientForNamespace(t, testTenantANamespace)
	resp, err := clientB.DeleteSessionWithResponse(context.Background(),
		&genapi.DeleteSessionParams{XCALMSessionToken: s.SessionToken})
	if err != nil {
		t.Fatalf("DeleteSession: %v", err)
	}
	if resp.StatusCode() != http.StatusNotFound {
		t.Fatalf("ns-b delete of ns-a session: status = %d; want 404 (invisibility-not-denial)", resp.StatusCode())
	}
	// Original session in ns-a must still exist.
	if n := countRows(
		t, env.sqlDB,
		`SELECT COUNT(*) FROM sessions WHERE id = $1`, s.ID,
	); n != 1 {
		t.Errorf("ns-a session row count = %d; want 1 (cross-namespace delete must not touch other ns)", n)
	}
}

// A session-delete request without X-CALM-API-Key returns 401; auth is enforced before session lookup.
func TestDeleteSessionHandler_MissingBearerReturns401(t *testing.T) {
	httpReq, _ := http.NewRequestWithContext(context.Background(), http.MethodDelete,
		env.serverURL+"/v1/sessions", http.NoBody)
	httpResp, err := http.DefaultClient.Do(httpReq)
	if err != nil {
		t.Fatalf("http: %v", err)
	}
	defer func() { _ = httpResp.Body.Close() }()
	if httpResp.StatusCode != http.StatusUnauthorized {
		t.Errorf("status = %d; want 401", httpResp.StatusCode)
	}
}

// A second delete of the same session token returns 404; session teardown is not idempotent in the "succeed silently" sense.
func TestDeleteSessionHandler_IdempotentSecondDeleteReturns404(t *testing.T) {
	seedClient(t, env.sqlDB, testNamespace, db.DefaultClient)
	s := seedSession(t, env.sqlDB, testNamespace, db.DefaultClient, 60)

	first, err := env.client.DeleteSessionWithResponse(context.Background(),
		&genapi.DeleteSessionParams{XCALMSessionToken: s.SessionToken})
	if err != nil {
		t.Fatalf("first DeleteSession: %v", err)
	}
	if first.StatusCode() != http.StatusNoContent {
		t.Fatalf("first call: status = %d; want 204", first.StatusCode())
	}
	second, err := env.client.DeleteSessionWithResponse(context.Background(),
		&genapi.DeleteSessionParams{XCALMSessionToken: s.SessionToken})
	if err != nil {
		t.Fatalf("second DeleteSession: %v", err)
	}
	if second.StatusCode() != http.StatusNotFound {
		t.Errorf("second call: status = %d; want 404", second.StatusCode())
	}
}

// ---------- Idempotency-Key dedup ----------

// Two create-session calls with the same Idempotency-Key return the same session token and write exactly one DB row; the workload sees a stable session reference across retries.
func TestCreateSessionHandler_IdempotencyKeySameKeyReturnsSameToken(t *testing.T) {
	key := "idem-" + randHex(8)

	first, err := env.client.CreateSessionWithResponse(
		context.Background(),
		&genapi.CreateSessionParams{IdempotencyKey: &key},
		genapi.CreateSessionJSONRequestBody{},
	)
	if err != nil {
		t.Fatalf("first CreateSession: %v", err)
	}
	if first.StatusCode() != http.StatusCreated {
		t.Fatalf("first: status = %d; want 201; body=%s", first.StatusCode(), string(first.Body))
	}
	original := first.JSON201.SessionToken

	second, err := env.client.CreateSessionWithResponse(
		context.Background(),
		&genapi.CreateSessionParams{IdempotencyKey: &key},
		genapi.CreateSessionJSONRequestBody{},
	)
	if err != nil {
		t.Fatalf("retry CreateSession: %v", err)
	}
	if second.StatusCode() != http.StatusCreated {
		t.Fatalf("retry: status = %d; want 201; body=%s", second.StatusCode(), string(second.Body))
	}
	if second.JSON201.SessionToken != original {
		t.Errorf("retry returned different session_token: %q vs original %q — dedup did not fire",
			second.JSON201.SessionToken, original)
	}

	// Only one DB row was written for the pair of calls.
	hash := auth.HashToken(testNamespace, original)
	n := countRows(t, env.sqlDB,
		`SELECT COUNT(*) FROM sessions WHERE namespace = $1 AND session_token_hash = $2`,
		testNamespace, hash)
	if n != 1 {
		t.Errorf("DB row count = %d; want 1 (dedup must not double-insert)", n)
	}
}

// Two distinct Idempotency-Keys produce two distinct session tokens; dedup scope is per-key, not per-workload.
func TestCreateSessionHandler_IdempotencyKeyDifferentKeyReturnsDifferentToken(t *testing.T) {
	keyA := "idem-" + randHex(8)
	keyB := "idem-" + randHex(8)

	first, err := env.client.CreateSessionWithResponse(
		context.Background(),
		&genapi.CreateSessionParams{IdempotencyKey: &keyA},
		genapi.CreateSessionJSONRequestBody{},
	)
	if err != nil {
		t.Fatalf("first CreateSession: %v", err)
	}
	second, err := env.client.CreateSessionWithResponse(
		context.Background(),
		&genapi.CreateSessionParams{IdempotencyKey: &keyB},
		genapi.CreateSessionJSONRequestBody{},
	)
	if err != nil {
		t.Fatalf("second CreateSession: %v", err)
	}
	if first.JSON201.SessionToken == second.JSON201.SessionToken {
		t.Errorf("distinct keys returned same token: dedup leaked across keys")
	}
}

// Without an Idempotency-Key every create-session call mints a distinct token; dedup is opt-in and does not apply implicitly.
func TestCreateSessionHandler_NoIdempotencyKeyMintsDistinctTokens(t *testing.T) {
	// Without a key, every call is fresh. Two back-to-back creates produce
	// two distinct sessions — proves dedup is opt-in, not implicit.
	first, err := env.client.CreateSessionWithResponse(
		context.Background(),
		&genapi.CreateSessionParams{},
		genapi.CreateSessionJSONRequestBody{},
	)
	if err != nil {
		t.Fatalf("first CreateSession: %v", err)
	}
	second, err := env.client.CreateSessionWithResponse(
		context.Background(),
		&genapi.CreateSessionParams{},
		genapi.CreateSessionJSONRequestBody{},
	)
	if err != nil {
		t.Fatalf("second CreateSession: %v", err)
	}
	if first.JSON201.SessionToken == second.JSON201.SessionToken {
		t.Errorf("two calls without Idempotency-Key returned the same token")
	}
}

// N concurrent retries with the same Idempotency-Key all receive the same session token and exactly one DB row is written; singleflight collapses the burst into one create.
func TestCreateSessionHandler_IdempotencyKeyConcurrentRetriesProduceOneSession(t *testing.T) {
	// N concurrent retries with the same key. Without singleflight, two or
	// more goroutines miss the LRU together, each mint+INSERT, and the DB
	// ends up with N session rows for one logical session. Singleflight
	// serializes them so only the first runs the work; the rest receive the
	// same dedupEntry.
	key := "concurrent-" + randHex(8)
	const callers = 16

	type result struct {
		token string
		err   error
	}
	results := make(chan result, callers)
	start := make(chan struct{})

	for i := 0; i < callers; i++ {
		go func() {
			<-start // synchronize the burst
			resp, err := env.client.CreateSessionWithResponse(
				context.Background(),
				&genapi.CreateSessionParams{IdempotencyKey: &key},
				genapi.CreateSessionJSONRequestBody{},
			)
			if err != nil {
				results <- result{err: err}
				return
			}
			if resp.StatusCode() != http.StatusCreated {
				results <- result{err: fmt.Errorf("status=%d body=%s", resp.StatusCode(), string(resp.Body))}
				return
			}
			results <- result{token: resp.JSON201.SessionToken}
		}()
	}

	close(start) // release the burst

	tokens := make(map[string]int, callers)
	for i := 0; i < callers; i++ {
		r := <-results
		if r.err != nil {
			t.Fatalf("caller %d: %v", i, r.err)
		}
		tokens[r.token]++
	}
	if len(tokens) != 1 {
		t.Errorf("concurrent retries with same Idempotency-Key returned %d distinct tokens; want 1 (singleflight failed): %v",
			len(tokens), tokens)
	}

	// Verify the DB has exactly one row for the dedup-collapsed session.
	var token string
	for k := range tokens {
		token = k
	}
	hash := auth.HashToken(testNamespace, token)
	if n := countRows(t, env.sqlDB,
		`SELECT COUNT(*) FROM sessions WHERE namespace = $1 AND session_token_hash = $2`,
		testNamespace, hash); n != 1 {
		t.Errorf("DB row count for collapsed session = %d; want 1", n)
	}
}

// A retry with the same Idempotency-Key returns the first call's committed fields regardless of what the retry request contains; the dedup entry is immutable after the first commit.
func TestCreateSessionHandler_IdempotencyKeyRetryReturnsSameCommittedFields(t *testing.T) {
	// A retry's response must echo the FIRST call's committed fields, not
	// the retry-time request fields. Specifically: a non-zero created_at,
	// the originally-committed ttl_minutes and client.
	key := "shape-" + randHex(8)
	originalTTL := 45

	first, err := env.client.CreateSessionWithResponse(
		context.Background(),
		&genapi.CreateSessionParams{IdempotencyKey: &key},
		genapi.CreateSessionJSONRequestBody{TtlMinutes: &originalTTL},
	)
	if err != nil {
		t.Fatalf("first CreateSession: %v", err)
	}
	if first.JSON201.CreatedAt.IsZero() {
		t.Fatalf("first response created_at is zero; sanity-check failed before retry")
	}

	// Retry with DIFFERENT request fields. Dedup should ignore them and
	// return the first call's committed values.
	differentTTL := 99
	second, err := env.client.CreateSessionWithResponse(
		context.Background(),
		&genapi.CreateSessionParams{IdempotencyKey: &key},
		genapi.CreateSessionJSONRequestBody{TtlMinutes: &differentTTL},
	)
	if err != nil {
		t.Fatalf("retry CreateSession: %v", err)
	}
	if second.JSON201.SessionToken != first.JSON201.SessionToken {
		t.Errorf("retry token mismatch: %q vs %q", second.JSON201.SessionToken, first.JSON201.SessionToken)
	}
	if !second.JSON201.CreatedAt.Equal(first.JSON201.CreatedAt) {
		t.Errorf("retry created_at = %v; want %v (original committed)", second.JSON201.CreatedAt, first.JSON201.CreatedAt)
	}
	if second.JSON201.TtlMinutes != first.JSON201.TtlMinutes {
		t.Errorf("retry ttl_minutes = %d; want %d (original committed, not retry-request %d)",
			second.JSON201.TtlMinutes, first.JSON201.TtlMinutes, differentTTL)
	}
	if second.JSON201.Client != first.JSON201.Client {
		t.Errorf("retry client = %q; want %q (original committed)", second.JSON201.Client, first.JSON201.Client)
	}
}
