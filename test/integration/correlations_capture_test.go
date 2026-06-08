// Copyright 2026 The CALM Authors
// SPDX-License-Identifier: Apache-2.0

package integration

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/google/uuid"

	"github.com/one-harsh/calm/internal/api/genapi"
)

// Each value-producing endpoint inserts a correlations row best-effort after
// success. These tests pin the contract by reading the row back via direct SQL
// against the response header's correlation_id.

func TestIngest_PersistsCorrelationOnSuccess(t *testing.T) {
	sess := createSessionForTest(t, testNamespace)
	client := env.clientForNamespace(t, testNamespace)

	resp, err := client.IngestWithResponse(context.Background(),
		&genapi.IngestParams{XCALMSessionToken: sess.SessionToken},
		genapi.IngestJSONRequestBody{Source: "capture.log", Content: "alpha\n\nbeta"})
	if err != nil {
		t.Fatalf("Ingest: %v", err)
	}
	if resp.StatusCode() != http.StatusOK {
		t.Fatalf("status = %d; body=%s", resp.StatusCode(), string(resp.Body))
	}

	correlationID := mustParseCorrelationID(t, resp.HTTPResponse.Header.Get("X-CALM-Correlation-Id"))
	row := readCorrelationRow(t, correlationID)

	if row.requestType != "ingest" {
		t.Errorf("request_type = %q; want ingest", row.requestType)
	}
	if row.outcome != "unset" {
		t.Errorf("outcome = %q; want unset (no feedback yet)", row.outcome)
	}
	if row.sessionID != sess.ID {
		t.Errorf("session_id = %d; want %d", row.sessionID, sess.ID)
	}
	if !hasKey(t, row.requestMeta, "sections_indexed") || !hasKey(t, row.requestMeta, "sections_total") {
		t.Errorf("request_meta = %s; want sections_indexed + sections_total", row.requestMeta)
	}
}

func TestSearch_PersistsCorrelationOnSuccess(t *testing.T) {
	sess := createSessionForTest(t, testNamespace)
	client := env.clientForNamespace(t, testNamespace)

	if _, err := client.IngestWithResponse(context.Background(),
		&genapi.IngestParams{XCALMSessionToken: sess.SessionToken},
		genapi.IngestJSONRequestBody{Source: "search-target", Content: "find me here"}); err != nil {
		t.Fatalf("seed ingest: %v", err)
	}

	resp, err := client.SearchWithResponse(context.Background(),
		&genapi.SearchParams{XCALMSessionToken: sess.SessionToken},
		genapi.SearchJSONRequestBody{Queries: []string{"find"}})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if resp.StatusCode() != http.StatusOK {
		t.Fatalf("status = %d; body=%s", resp.StatusCode(), string(resp.Body))
	}

	correlationID := mustParseCorrelationID(t, resp.HTTPResponse.Header.Get("X-CALM-Correlation-Id"))
	row := readCorrelationRow(t, correlationID)

	if row.requestType != "search" {
		t.Errorf("request_type = %q; want search", row.requestType)
	}
	if row.sessionID != sess.ID {
		t.Errorf("session_id = %d; want %d", row.sessionID, sess.ID)
	}
	if !hasKey(t, row.requestMeta, "hit_count") {
		t.Errorf("request_meta = %s; want hit_count", row.requestMeta)
	}
}

func TestSnapshot_PersistsCorrelationOnSuccess(t *testing.T) {
	sess := createSessionForTest(t, testNamespace)
	client := env.clientForNamespace(t, testNamespace)
	seedEvent(t, env.sqlDB, sess.ID, "tool_invocation", 2, []byte(`{"command":"ls"}`))

	resp, err := client.GetSnapshotWithResponse(context.Background(),
		&genapi.GetSnapshotParams{XCALMSessionToken: sess.SessionToken})
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	if resp.StatusCode() != http.StatusOK {
		t.Fatalf("status = %d; body=%s", resp.StatusCode(), string(resp.Body))
	}

	correlationID := mustParseCorrelationID(t, resp.HTTPResponse.Header.Get("X-CALM-Correlation-Id"))
	row := readCorrelationRow(t, correlationID)

	if row.requestType != "snapshot" {
		t.Errorf("request_type = %q; want snapshot", row.requestType)
	}
	if row.sessionID != sess.ID {
		t.Errorf("session_id = %d; want %d", row.sessionID, sess.ID)
	}
	if !hasKey(t, row.requestMeta, "byte_budget_used") {
		t.Errorf("request_meta = %s; want byte_budget_used", row.requestMeta)
	}
}

type correlationRow struct {
	sessionID   int64
	requestType string
	requestMeta []byte
	outcome     string
}

func readCorrelationRow(t *testing.T, correlationID uuid.UUID) correlationRow {
	t.Helper()
	var row correlationRow
	if err := env.sqlDB.QueryRowContext(
		context.Background(),
		`SELECT session_id, request_type, request_meta, outcome FROM correlations WHERE correlation_id = $1`,
		correlationID[:],
	).Scan(&row.sessionID, &row.requestType, &row.requestMeta, &row.outcome); err != nil {
		t.Fatalf("read correlation %s: %v", correlationID, err)
	}
	return row
}

func mustParseCorrelationID(t *testing.T, raw string) uuid.UUID {
	t.Helper()
	if raw == "" {
		t.Fatal("X-CALM-Correlation-Id header missing")
	}
	id, err := uuid.Parse(raw)
	if err != nil {
		t.Fatalf("parse correlation id %q: %v", raw, err)
	}
	return id
}

func hasKey(t *testing.T, meta []byte, key string) bool {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(meta, &m); err != nil {
		t.Fatalf("unmarshal request_meta: %v", err)
	}
	_, ok := m[key]
	return ok
}
