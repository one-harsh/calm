// Copyright 2026 The CALM Authors
// SPDX-License-Identifier: Apache-2.0

package integration

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/one-harsh/calm/internal/api/genapi"
	"github.com/one-harsh/calm/internal/db"
)

func TestCreateSessionHandler_HappyMinimal(t *testing.T) {
	resp, err := env.client.CreateSessionWithResponse(context.Background(),
		genapi.CreateSessionJSONRequestBody{SessionId: "s1"},
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
	if resp.JSON201.SessionId != "s1" {
		t.Errorf("session_id = %q; want s1", resp.JSON201.SessionId)
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

	// DB row exists.
	n := countRows(t, env.sqlDB,
		`SELECT COUNT(*) FROM sessions WHERE namespace = $1 AND session_id = $2`,
		testNamespace, "s1")
	if n != 1 {
		t.Errorf("DB row count = %d; want 1", n)
	}
}

func TestCreateSessionHandler_HappyWithAllFields(t *testing.T) {
	client := "alice"
	ttl := 60
	labels := map[string]string{"env": "prod", "tier": "1"}
	resp, err := env.client.CreateSessionWithResponse(context.Background(),
		genapi.CreateSessionJSONRequestBody{
			SessionId:  "s-all-fields",
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
	if labelCount := countRows(t, env.sqlDB,
		`SELECT COUNT(*) FROM session_labels WHERE namespace = $1 AND session_id = $2`,
		testNamespace, "s-all-fields"); labelCount != 2 {
		t.Errorf("label rows = %d; want 2", labelCount)
	}
}

func TestCreateSessionHandler_AutoRegistersNewClient(t *testing.T) {
	client := "freshly-introduced-client"
	resp, err := env.client.CreateSessionWithResponse(context.Background(),
		genapi.CreateSessionJSONRequestBody{
			SessionId: "s-auto-reg",
			Client:    &client,
		},
	)
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	if resp.StatusCode() != http.StatusCreated {
		t.Fatalf("status = %d; want 201; body=%s", resp.StatusCode(), string(resp.Body))
	}
	n := countRows(t, env.sqlDB,
		`SELECT COUNT(*) FROM clients WHERE namespace = $1 AND name = $2`,
		testNamespace, client)
	if n != 1 {
		t.Errorf("client row count = %d; want 1 (Service.Create's WithTx auto-register must flow through the handler)", n)
	}
}

func TestCreateSessionHandler_DefaultsEmptyClient(t *testing.T) {
	resp, err := env.client.CreateSessionWithResponse(context.Background(),
		genapi.CreateSessionJSONRequestBody{SessionId: "s-default-client"},
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

func TestCreateSessionHandler_AbsentTTLUsesConfigInactivityValue(t *testing.T) {
	resp, err := env.client.CreateSessionWithResponse(context.Background(),
		genapi.CreateSessionJSONRequestBody{SessionId: "s-default-ttl"},
	)
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	if resp.JSON201.TtlMinutes != testDefaultTTLMinutes {
		t.Errorf("ttl_minutes = %d; want %d (config DefaultTTLMinutes fallback)",
			resp.JSON201.TtlMinutes, testDefaultTTLMinutes)
	}
}

func TestCreateSessionHandler_DuplicateSessionIDSameNamespaceReturns409(t *testing.T) {
	body := genapi.CreateSessionJSONRequestBody{SessionId: "s-dup"}
	first, err := env.client.CreateSessionWithResponse(context.Background(), body)
	if err != nil {
		t.Fatalf("first CreateSession: %v", err)
	}
	if first.StatusCode() != http.StatusCreated {
		t.Fatalf("first call: status = %d; want 201", first.StatusCode())
	}
	second, err := env.client.CreateSessionWithResponse(context.Background(), body)
	if err != nil {
		t.Fatalf("second CreateSession: %v", err)
	}
	if second.StatusCode() != http.StatusConflict {
		t.Fatalf("second call: status = %d; want 409", second.StatusCode())
	}
	if second.JSON409 == nil {
		t.Fatal("JSON409 nil on 409 response")
	}
	if second.JSON409.Error != "session_exists" {
		t.Errorf("error code = %q; want session_exists", second.JSON409.Error)
	}
	if second.JSON409.Detail == nil || !strings.Contains(*second.JSON409.Detail, "s-dup") {
		t.Errorf("detail %v should mention session id", second.JSON409.Detail)
	}
}

func TestCreateSessionHandler_SameSessionIDCrossNamespacesAllowed(t *testing.T) {
	clientA := env.clientForNamespace(t, testNamespace)
	clientB := env.clientForNamespace(t, testTenantANamespace)

	body := genapi.CreateSessionJSONRequestBody{SessionId: "shared-id-xns"}

	respA, err := clientA.CreateSessionWithResponse(context.Background(), body)
	if err != nil {
		t.Fatalf("CreateSession in %s: %v", testNamespace, err)
	}
	if respA.StatusCode() != http.StatusCreated {
		t.Fatalf("ns-a status = %d; want 201", respA.StatusCode())
	}
	if respA.JSON201.Namespace != testNamespace {
		t.Errorf("ns-a response.namespace = %q; want %q", respA.JSON201.Namespace, testNamespace)
	}

	respB, err := clientB.CreateSessionWithResponse(context.Background(), body)
	if err != nil {
		t.Fatalf("CreateSession in %s: %v", testTenantANamespace, err)
	}
	if respB.StatusCode() != http.StatusCreated {
		t.Fatalf("ns-b status = %d; want 201 (same session_id across namespaces must succeed)", respB.StatusCode())
	}
	if respB.JSON201.Namespace != testTenantANamespace {
		t.Errorf("ns-b response.namespace = %q; want %q", respB.JSON201.Namespace, testTenantANamespace)
	}

	// Both rows present, scoped per namespace.
	if n := countRows(t, env.sqlDB,
		`SELECT COUNT(*) FROM sessions WHERE session_id = $1`,
		"shared-id-xns"); n != 2 {
		t.Errorf("session count across namespaces = %d; want 2", n)
	}
}

func TestCreateSessionHandler_MissingBearerReturns401(t *testing.T) {
	// Raw HTTP — bypassing the typed client (which always attaches the bearer).
	httpReq, _ := http.NewRequestWithContext(context.Background(), http.MethodPost,
		env.serverURL+"/v1/sessions",
		bytes.NewBufferString(`{"session_id":"s-no-auth"}`))
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

func TestCreateSessionHandler_RequestedTTLAboveOperatorMaxClamped(t *testing.T) {
	// testMaxTTLMinutes is 240; OpenAPI cap is 10080. A value between them
	// passes the validator and gets clamped by the handler. Response echoes
	// the committed (clamped) value.
	requested := 5000
	resp, err := env.client.CreateSessionWithResponse(context.Background(),
		genapi.CreateSessionJSONRequestBody{
			SessionId:  "s-clamp",
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

func TestCreateSessionHandler_OverMaxTTLRejected400(t *testing.T) {
	overMax := 20_000 // > OpenAPI max of 10080
	resp, err := env.client.CreateSessionWithResponse(context.Background(),
		genapi.CreateSessionJSONRequestBody{
			SessionId:  "s-over-max",
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

func TestCreateSessionHandler_ResponseHasNoLabelsField(t *testing.T) {
	labels := map[string]string{"x": "y"}
	resp, err := env.client.CreateSessionWithResponse(context.Background(),
		genapi.CreateSessionJSONRequestBody{
			SessionId: "s-no-labels-echo",
			Labels:    &labels,
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

func TestNotImplementedHandlersStillReturn501(t *testing.T) {
	httpReq, _ := http.NewRequestWithContext(context.Background(), http.MethodGet,
		env.serverURL+"/v1/version", http.NoBody)
	httpReq.Header.Set("Authorization", "Bearer "+testMasterKey)
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

func TestDeleteSessionHandler_HappyDeletesSessionWithCascade(t *testing.T) {
	seedClient(t, env.sqlDB, testNamespace, db.DefaultClient)
	seedSession(t, env.sqlDB, testNamespace, db.DefaultClient, "del-happy", 60)
	seedSessionLabel(t, env.sqlDB, testNamespace, "del-happy", "env", "prod")
	seedSessionLabel(t, env.sqlDB, testNamespace, "del-happy", "tier", "1")
	src := seedSource(t, env.sqlDB, testNamespace, "del-happy", "spec.md")
	seedChunk(t, env.sqlDB, src, "Section 1", "body", "prose")
	seedChunk(t, env.sqlDB, src, "Section 2", "body", "prose")
	seedEvent(t, env.sqlDB, testNamespace, "del-happy", "ingest", 1, []byte(`{"x":1}`))

	resp, err := env.client.DeleteSessionWithResponse(context.Background(), "del-happy")
	if err != nil {
		t.Fatalf("DeleteSession: %v", err)
	}
	if resp.StatusCode() != http.StatusOK {
		t.Fatalf("status = %d; want 200; body=%s", resp.StatusCode(), string(resp.Body))
	}
	if resp.JSON200 == nil {
		t.Fatal("JSON200 nil on 200")
	}
	if resp.JSON200.DeletedSessionId != "del-happy" {
		t.Errorf("deleted_session_id = %q; want del-happy", resp.JSON200.DeletedSessionId)
	}
	if resp.JSON200.Cascaded.Labels != 2 {
		t.Errorf("cascaded.labels = %d; want 2", resp.JSON200.Cascaded.Labels)
	}
	if resp.JSON200.Cascaded.Sources != 1 {
		t.Errorf("cascaded.sources = %d; want 1", resp.JSON200.Cascaded.Sources)
	}
	if resp.JSON200.Cascaded.Chunks != 2 {
		t.Errorf("cascaded.chunks = %d; want 2", resp.JSON200.Cascaded.Chunks)
	}
	if resp.JSON200.Cascaded.Events != 1 {
		t.Errorf("cascaded.events = %d; want 1", resp.JSON200.Cascaded.Events)
	}

	for _, tbl := range []string{"sessions", "session_labels", "sources", "session_events"} {
		if n := countRows(t, env.sqlDB,
			"SELECT COUNT(*) FROM "+tbl+" WHERE namespace = $1 AND session_id = $2",
			testNamespace, "del-happy",
		); n != 0 {
			t.Errorf("DB row count in %s = %d; want 0 after delete", tbl, n)
		}
	}

	// HLD explicit-close requirement: clients.last_activity_at bumped so
	// post-teardown observability sees the client's most-recent activity.
	if n := countRows(t, env.sqlDB,
		`SELECT COUNT(*) FROM clients WHERE namespace = $1 AND name = $2 AND last_activity_at IS NOT NULL`,
		testNamespace, db.DefaultClient,
	); n != 1 {
		t.Errorf("clients.last_activity_at not set after explicit close: matching-rows = %d; want 1", n)
	}
}

func TestDeleteSessionHandler_UnknownSessionReturns404(t *testing.T) {
	resp, err := env.client.DeleteSessionWithResponse(context.Background(), "never-existed")
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
	if resp.JSON404.Detail == nil || !strings.Contains(*resp.JSON404.Detail, "never-existed") {
		t.Errorf("detail %v should mention session id", resp.JSON404.Detail)
	}
}

func TestDeleteSessionHandler_CrossNamespaceReturns404(t *testing.T) {
	seedClient(t, env.sqlDB, testNamespace, db.DefaultClient)
	seedSession(t, env.sqlDB, testNamespace, db.DefaultClient, "xns-1", 60)

	clientB := env.clientForNamespace(t, testTenantANamespace)
	resp, err := clientB.DeleteSessionWithResponse(context.Background(), "xns-1")
	if err != nil {
		t.Fatalf("DeleteSession: %v", err)
	}
	if resp.StatusCode() != http.StatusNotFound {
		t.Fatalf("ns-b delete of ns-a session: status = %d; want 404 (invisibility-not-denial)", resp.StatusCode())
	}
	// Original session in ns-a must still exist.
	if n := countRows(t, env.sqlDB,
		`SELECT COUNT(*) FROM sessions WHERE namespace = $1 AND session_id = $2`,
		testNamespace, "xns-1",
	); n != 1 {
		t.Errorf("ns-a/xns-1 row count = %d; want 1 (cross-namespace delete must not touch other ns)", n)
	}
}

func TestDeleteSessionHandler_SameSessionIDDifferentNamespacesIndependent(t *testing.T) {
	seedClient(t, env.sqlDB, testNamespace, db.DefaultClient)
	seedClient(t, env.sqlDB, testTenantANamespace, db.DefaultClient)
	seedSession(t, env.sqlDB, testNamespace, db.DefaultClient, "shared-del", 60)
	seedSession(t, env.sqlDB, testTenantANamespace, db.DefaultClient, "shared-del", 60)

	resp, err := env.client.DeleteSessionWithResponse(context.Background(), "shared-del")
	if err != nil {
		t.Fatalf("DeleteSession in default: %v", err)
	}
	if resp.StatusCode() != http.StatusOK {
		t.Fatalf("status = %d; want 200", resp.StatusCode())
	}

	if n := countRows(t, env.sqlDB,
		`SELECT COUNT(*) FROM sessions WHERE namespace = $1 AND session_id = $2`,
		testNamespace, "shared-del",
	); n != 0 {
		t.Errorf("default/shared-del row count after delete = %d; want 0", n)
	}
	if n := countRows(t, env.sqlDB,
		`SELECT COUNT(*) FROM sessions WHERE namespace = $1 AND session_id = $2`,
		testTenantANamespace, "shared-del",
	); n != 1 {
		t.Errorf("tenant-a/shared-del row count = %d; want 1 (other-namespace session must survive)", n)
	}
}

func TestDeleteSessionHandler_MissingBearerReturns401(t *testing.T) {
	httpReq, _ := http.NewRequestWithContext(context.Background(), http.MethodDelete,
		env.serverURL+"/v1/sessions/whatever", http.NoBody)
	httpResp, err := http.DefaultClient.Do(httpReq)
	if err != nil {
		t.Fatalf("http: %v", err)
	}
	defer func() { _ = httpResp.Body.Close() }()
	if httpResp.StatusCode != http.StatusUnauthorized {
		t.Errorf("status = %d; want 401", httpResp.StatusCode)
	}
}

func TestDeleteSessionHandler_IdempotentSecondDeleteReturns404(t *testing.T) {
	seedClient(t, env.sqlDB, testNamespace, db.DefaultClient)
	seedSession(t, env.sqlDB, testNamespace, db.DefaultClient, "double-del", 60)

	first, err := env.client.DeleteSessionWithResponse(context.Background(), "double-del")
	if err != nil {
		t.Fatalf("first DeleteSession: %v", err)
	}
	if first.StatusCode() != http.StatusOK {
		t.Fatalf("first call: status = %d; want 200", first.StatusCode())
	}
	second, err := env.client.DeleteSessionWithResponse(context.Background(), "double-del")
	if err != nil {
		t.Fatalf("second DeleteSession: %v", err)
	}
	if second.StatusCode() != http.StatusNotFound {
		t.Errorf("second call: status = %d; want 404", second.StatusCode())
	}
}
