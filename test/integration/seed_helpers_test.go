// Copyright 2026 The CALM Authors
// SPDX-License-Identifier: Apache-2.0

package integration

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/one-harsh/calm/internal/auth"
	"github.com/one-harsh/calm/internal/db"
)

// Fixture helpers. Each takes the sibling *sql.DB (not the under-test *db.Store)
// so tests bypass the surface they're verifying.

// seededSession carries the surrogate id of a freshly-seeded session plus the
// raw session_token a test can present to handler/service-level entry points
// that authenticate by token. Tests that only need the FK target use .ID;
// tests that exercise the auth boundary use .SessionToken.
type seededSession struct {
	ID           int64
	SessionToken string
	Namespace    string
	Client       string
}

func seedClient(t *testing.T, sqlDB *sql.DB, namespace, name string) {
	t.Helper()
	if _, err := sqlDB.ExecContext(context.Background(),
		`INSERT INTO clients (namespace, name) VALUES ($1, $2) ON CONFLICT DO NOTHING`,
		namespace, name,
	); err != nil {
		t.Fatalf("seedClient(%q/%q): %v", namespace, name, err)
	}
}

// seedSession inserts a session with a freshly-minted token and returns the
// surrogate id alongside the raw token. The caller picks either the id (for
// child-row FK references / cascade assertions) or the token (for service /
// handler entry points).
func seedSession(t *testing.T, sqlDB *sql.DB, namespace, client string, ttlMinutes int) seededSession {
	t.Helper()
	raw, err := auth.NewRandomToken()
	if err != nil {
		t.Fatalf("seedSession mint token: %v", err)
	}
	hash := auth.HashToken(namespace, raw)
	var id int64
	if err := sqlDB.QueryRowContext(context.Background(),
		`INSERT INTO sessions (namespace, client, session_token_hash, ttl_minutes)
		 VALUES ($1, $2, $3, $4) RETURNING id`,
		namespace, client, hash, ttlMinutes,
	).Scan(&id); err != nil {
		t.Fatalf("seedSession(%q/%q): %v", namespace, client, err)
	}
	return seededSession{ID: id, SessionToken: raw, Namespace: namespace, Client: client}
}

// seedSessionWithActivity is seedSession + an explicit last_activity stamp
// (the trigger derives expires_at from it). Used by TTL-scanner tests that
// need predictable expiry.
func seedSessionWithActivity(t *testing.T, sqlDB *sql.DB, namespace, client string, ttlMinutes int, lastActivity time.Time) seededSession {
	t.Helper()
	raw, err := auth.NewRandomToken()
	if err != nil {
		t.Fatalf("seedSessionWithActivity mint token: %v", err)
	}
	hash := auth.HashToken(namespace, raw)
	var id int64
	if err := sqlDB.QueryRowContext(context.Background(),
		`INSERT INTO sessions (namespace, client, session_token_hash, ttl_minutes, last_activity)
		 VALUES ($1, $2, $3, $4, $5) RETURNING id`,
		namespace, client, hash, ttlMinutes, lastActivity,
	).Scan(&id); err != nil {
		t.Fatalf("seedSessionWithActivity(%q/%q): %v", namespace, client, err)
	}
	return seededSession{ID: id, SessionToken: raw, Namespace: namespace, Client: client}
}

func seedSessionLabel(t *testing.T, sqlDB *sql.DB, sessionID int64, key, value string) {
	t.Helper()
	if _, err := sqlDB.ExecContext(context.Background(),
		`INSERT INTO session_labels (session_id, key, value) VALUES ($1, $2, $3)`,
		sessionID, key, value,
	); err != nil {
		t.Fatalf("seedSessionLabel(session=%d, %q=%q): %v", sessionID, key, value, err)
	}
}

func seedSource(t *testing.T, sqlDB *sql.DB, sessionID int64, label string) int64 {
	t.Helper()
	var id int64
	if err := sqlDB.QueryRowContext(context.Background(),
		`INSERT INTO sources (session_id, label) VALUES ($1, $2) RETURNING id`,
		sessionID, label,
	).Scan(&id); err != nil {
		t.Fatalf("seedSource(session=%d, label=%q): %v", sessionID, label, err)
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

func seedEvent(t *testing.T, sqlDB *sql.DB, sessionID int64, eventType string, priority int, data []byte) {
	t.Helper()
	if _, err := sqlDB.ExecContext(context.Background(),
		`INSERT INTO session_events (session_id, type, priority, data, data_hash) VALUES ($1, $2, $3, $4, $5)`,
		sessionID, eventType, priority, data, db.HashEventPayload(eventType, data),
	); err != nil {
		t.Fatalf("seedEvent(session=%d, type=%q): %v", sessionID, eventType, err)
	}
}

// seedEventAt is seedEvent + an explicit created_at stamp. The schema default
// is now(), so this is the only way to seed events with controlled ordering.
func seedEventAt(t *testing.T, sqlDB *sql.DB, sessionID int64, eventType string, priority int, data []byte, createdAt time.Time) {
	t.Helper()
	if _, err := sqlDB.ExecContext(context.Background(),
		`INSERT INTO session_events (session_id, type, priority, data, data_hash, created_at) VALUES ($1, $2, $3, $4, $5, $6)`,
		sessionID, eventType, priority, data, db.HashEventPayload(eventType, data), createdAt,
	); err != nil {
		t.Fatalf("seedEventAt(session=%d, type=%q, at=%v): %v", sessionID, eventType, createdAt, err)
	}
}

func seedVocab(t *testing.T, sqlDB *sql.DB, sessionID int64, word string, docFreq int) {
	t.Helper()
	if _, err := sqlDB.ExecContext(context.Background(),
		`INSERT INTO vocabulary (session_id, word, doc_freq) VALUES ($1, $2, $3)`,
		sessionID, word, docFreq,
	); err != nil {
		t.Fatalf("seedVocab(session=%d, word=%q): %v", sessionID, word, err)
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
