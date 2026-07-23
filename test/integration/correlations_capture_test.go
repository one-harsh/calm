// Copyright 2026 The CALM Authors
// SPDX-License-Identifier: Apache-2.0

package integration

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/one-harsh/calm/internal/api/genapi"
)

// Each value-producing endpoint inserts a correlations row best-effort after
// success. These tests pin the contract by reading the row back via direct SQL
// against the response header's correlation_id.

// A successful ingest produces a correlation row with request_type=ingest,
// outcome=unset, and request_meta containing sections_indexed + sections_total.
func TestIngest_PersistsCorrelationOnSuccess(t *testing.T) {
	t.Parallel()
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

// A successful search produces a correlation row with request_type=search and
// request_meta containing hit_count.
func TestSearch_PersistsCorrelationOnSuccess(t *testing.T) {
	t.Parallel()
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
	for _, k := range []string{"allocator", "budget_exceeded", "byte_budget_used", "results_omitted"} {
		if !hasKey(t, row.requestMeta, k) {
			t.Errorf("request_meta = %s; want key %q", row.requestMeta, k)
		}
	}
	// A short chunk with a literal term match is not a fallback.
	if got := metaInt(t, row.requestMeta, "snippet_fallbacks"); got != 0 {
		t.Errorf("snippet_fallbacks = %d; want 0 on a clean match", got)
	}
}

// A search whose query matches only stemmed variants (no literal substring in an
// over-budget chunk) records the snippet as a leading-window fallback: the
// correlation row's snippet_fallbacks dimension is >= 1. This is the instrumented
// signal that gates the future lexeme-expansion locator upgrade.
func TestSearch_StemmedMissRecordedAsSnippetFallback(t *testing.T) {
	t.Parallel()
	sess := createSessionForTest(t, testNamespace)
	client := env.clientForNamespace(t, testNamespace)

	// Over-budget prose whose only relation to "caching" is the stem "cached";
	// "caching" never appears literally, so the locator finds no site.
	pad := strings.Repeat("Background paragraph that fills the chunk beyond the budget. ", 10)
	content := pad + "The lookup result was cached for later reuse. " + pad

	if _, err := client.IngestWithResponse(context.Background(),
		&genapi.IngestParams{XCALMSessionToken: sess.SessionToken},
		genapi.IngestJSONRequestBody{Source: "notes.md", Content: content}); err != nil {
		t.Fatalf("seed ingest: %v", err)
	}

	resp, err := client.SearchWithResponse(context.Background(),
		&genapi.SearchParams{XCALMSessionToken: sess.SessionToken},
		genapi.SearchJSONRequestBody{Queries: []string{"caching"}})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if resp.StatusCode() != http.StatusOK {
		t.Fatalf("status = %d; body=%s", resp.StatusCode(), string(resp.Body))
	}

	correlationID := mustParseCorrelationID(t, resp.HTTPResponse.Header.Get("X-CALM-Correlation-Id"))
	row := readCorrelationRow(t, correlationID)
	if got := metaInt(t, row.requestMeta, "snippet_fallbacks"); got < 1 {
		t.Errorf("snippet_fallbacks = %d; want >= 1 for a stemmed miss over budget", got)
	}
}

// A successful snapshot produces a correlation row with request_type=snapshot
// and request_meta containing byte_budget_used.
func TestSnapshot_PersistsCorrelationOnSuccess(t *testing.T) {
	t.Parallel()
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

func metaInt(t *testing.T, meta []byte, key string) int {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(meta, &m); err != nil {
		t.Fatalf("unmarshal request_meta: %v", err)
	}
	v, ok := m[key]
	if !ok {
		t.Fatalf("request_meta = %s; missing key %q", meta, key)
	}
	f, ok := v.(float64)
	if !ok {
		t.Fatalf("request_meta[%q] = %v; want a number", key, v)
	}
	return int(f)
}
