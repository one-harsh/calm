// Copyright 2026 The CALM Authors
// SPDX-License-Identifier: Apache-2.0

package integration

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"testing"
	"time"
)

// Fixture helpers. Each takes the sibling *sql.DB (not the under-test *db.Store)
// so tests bypass the surface they're verifying.

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

func seedSessionWithActivity(t *testing.T, sqlDB *sql.DB, namespace, client, sessionID string, ttlMinutes int, lastActivity time.Time) {
	t.Helper()
	if _, err := sqlDB.ExecContext(context.Background(),
		`INSERT INTO sessions (session_id, namespace, client, ttl_minutes, last_activity) VALUES ($1, $2, $3, $4, $5)`,
		sessionID, namespace, client, ttlMinutes, lastActivity,
	); err != nil {
		t.Fatalf("seedSessionWithActivity(%q): %v", sessionID, err)
	}
}

func seedSessionLabel(t *testing.T, sqlDB *sql.DB, namespace, sessionID, key, value string) {
	t.Helper()
	if _, err := sqlDB.ExecContext(context.Background(),
		`INSERT INTO session_labels (namespace, session_id, key, value) VALUES ($1, $2, $3, $4)`,
		namespace, sessionID, key, value,
	); err != nil {
		t.Fatalf("seedSessionLabel(%q/%q[%q]=%q): %v", namespace, sessionID, key, value, err)
	}
}

func seedSource(t *testing.T, sqlDB *sql.DB, namespace, sessionID, label string) int64 {
	t.Helper()
	var id int64
	if err := sqlDB.QueryRowContext(context.Background(),
		`INSERT INTO sources (namespace, session_id, label) VALUES ($1, $2, $3) RETURNING id`,
		namespace, sessionID, label,
	).Scan(&id); err != nil {
		t.Fatalf("seedSource(%q in %q/%q): %v", label, namespace, sessionID, err)
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

func seedEvent(t *testing.T, sqlDB *sql.DB, namespace, sessionID, eventType string, priority int, data []byte) {
	t.Helper()
	h := sha256.Sum256(append([]byte(eventType), data...))
	if _, err := sqlDB.ExecContext(context.Background(),
		`INSERT INTO session_events (namespace, session_id, type, priority, data, data_hash) VALUES ($1, $2, $3, $4, $5, $6)`,
		namespace, sessionID, eventType, priority, data, h[:],
	); err != nil {
		t.Fatalf("seedEvent(%q/%q type=%q): %v", namespace, sessionID, eventType, err)
	}
}

func seedVocab(t *testing.T, sqlDB *sql.DB, namespace, sessionID, word string, docFreq int) {
	t.Helper()
	if _, err := sqlDB.ExecContext(context.Background(),
		`INSERT INTO vocabulary (namespace, session_id, word, doc_freq) VALUES ($1, $2, $3, $4)`,
		namespace, sessionID, word, docFreq,
	); err != nil {
		t.Fatalf("seedVocab(%q/%q word=%q): %v", namespace, sessionID, word, err)
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
