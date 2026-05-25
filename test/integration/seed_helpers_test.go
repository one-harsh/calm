// Copyright 2026 The CALM Authors
// SPDX-License-Identifier: Apache-2.0

package integration

import (
	"context"
	"database/sql"
	"testing"
	"time"
)

// Fixture helpers. Each takes the sibling *sql.DB (not the under-test *db.Store)
// so tests bypass the surface they're verifying. Reused by WI-04+ as DAL
// coverage expands.

func seedClient(t *testing.T, sqlDB *sql.DB, namespace, name string) {
	t.Helper()
	if _, err := sqlDB.ExecContext(context.Background(),
		`INSERT INTO clients (namespace, name) VALUES ($1, $2) ON CONFLICT DO NOTHING`,
		namespace, name,
	); err != nil {
		t.Fatalf("seedClient(%q/%q): %v", namespace, name, err)
	}
}

func seedSession(t *testing.T, sqlDB *sql.DB, namespace, client, sessionID string, ttlMinutes int) {
	t.Helper()
	if _, err := sqlDB.ExecContext(context.Background(),
		`INSERT INTO sessions (session_id, namespace, client, ttl_minutes) VALUES ($1, $2, $3, $4)`,
		sessionID, namespace, client, ttlMinutes,
	); err != nil {
		t.Fatalf("seedSession(%q in %q/%q): %v", sessionID, namespace, client, err)
	}
}

// seedSessionWithActivity inserts a session with a controlled last_activity
// timestamp — useful for testing ListClients' MAX(last_activity) aggregation.
func seedSessionWithActivity(t *testing.T, sqlDB *sql.DB, namespace, client, sessionID string, ttlMinutes int, lastActivity time.Time) {
	t.Helper()
	if _, err := sqlDB.ExecContext(context.Background(),
		`INSERT INTO sessions (session_id, namespace, client, ttl_minutes, last_activity) VALUES ($1, $2, $3, $4, $5)`,
		sessionID, namespace, client, ttlMinutes, lastActivity,
	); err != nil {
		t.Fatalf("seedSessionWithActivity(%q): %v", sessionID, err)
	}
}

func seedSessionLabel(t *testing.T, sqlDB *sql.DB, sessionID, key, value string) {
	t.Helper()
	if _, err := sqlDB.ExecContext(context.Background(),
		`INSERT INTO session_labels (session_id, key, value) VALUES ($1, $2, $3)`,
		sessionID, key, value,
	); err != nil {
		t.Fatalf("seedSessionLabel(%q[%q]=%q): %v", sessionID, key, value, err)
	}
}

func seedSource(t *testing.T, sqlDB *sql.DB, sessionID, label string) int64 {
	t.Helper()
	var id int64
	if err := sqlDB.QueryRowContext(context.Background(),
		`INSERT INTO sources (session_id, label) VALUES ($1, $2) RETURNING id`,
		sessionID, label,
	).Scan(&id); err != nil {
		t.Fatalf("seedSource(%q in %q): %v", label, sessionID, err)
	}
	return id
}

func seedChunk(t *testing.T, sqlDB *sql.DB, sourceID int64, title, content, contentType string) {
	t.Helper()
	if _, err := sqlDB.ExecContext(context.Background(),
		`INSERT INTO chunks (source_id, title, content, content_type) VALUES ($1, $2, $3, $4)`,
		sourceID, title, content, contentType,
	); err != nil {
		t.Fatalf("seedChunk(source=%d, title=%q): %v", sourceID, title, err)
	}
}

func seedEvent(t *testing.T, sqlDB *sql.DB, sessionID, eventType string, priority int, data []byte) {
	t.Helper()
	if _, err := sqlDB.ExecContext(context.Background(),
		`INSERT INTO session_events (session_id, type, priority, data, data_hash) VALUES ($1, $2, $3, $4, $5)`,
		sessionID, eventType, priority, data, "test-hash-"+eventType,
	); err != nil {
		t.Fatalf("seedEvent(%q type=%q): %v", sessionID, eventType, err)
	}
}

func seedVocab(t *testing.T, sqlDB *sql.DB, sessionID, word string, docFreq int) {
	t.Helper()
	if _, err := sqlDB.ExecContext(context.Background(),
		`INSERT INTO vocabulary (session_id, word, doc_freq) VALUES ($1, $2, $3)`,
		sessionID, word, docFreq,
	); err != nil {
		t.Fatalf("seedVocab(%q word=%q): %v", sessionID, word, err)
	}
}

// countRows is a small helper for negative-presence assertions ("after
// delete, this table has 0 rows matching this client").
func countRows(t *testing.T, sqlDB *sql.DB, query string, args ...any) int {
	t.Helper()
	var n int
	if err := sqlDB.QueryRowContext(context.Background(), query, args...).Scan(&n); err != nil {
		t.Fatalf("countRows(%q): %v", query, err)
	}
	return n
}
