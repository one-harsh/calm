// Copyright 2026 The CALM Authors
// SPDX-License-Identifier: Apache-2.0

package db

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/binary"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5/pgconn"
	logging "github.com/one-harsh/context-logging"
)

const defaultReadEventsLimit = 100

type eventsRepo struct {
	queryer queryer
	logger  *logging.Logger
}

// HLD-DEVIATION: the last-N dedup window required by HLD's storage section
// is not enforced — every event is written as-is. The per-session FIFO cap
// + lowest-priority-oldest-first eviction from the same section is also
// not enforced; sessions can grow unbounded. data_hash is still computed
// and persisted so reconciliation only adds a SELECT-before-insert branch
// + post-commit cap check; no schema migration is owed.
func (r *eventsRepo) Write(ctx context.Context, namespace string, sessionID int64, events []EventInput) (int, error) {
	if namespace == "" {
		return 0, ErrNamespaceRequired
	}
	if len(events) == 0 {
		return 0, nil
	}
	for i := range events {
		if events[i].Priority < 1 || events[i].Priority > 4 {
			return 0, ErrInvalidPriority
		}
	}

	placeholders := make([]string, 0, len(events))
	args := make([]any, 0, len(events)*5)
	pos := 1
	for _, e := range events {
		placeholders = append(placeholders,
			fmt.Sprintf("($%d, $%d, $%d, $%d, $%d)", pos, pos+1, pos+2, pos+3, pos+4))
		args = append(args, sessionID, e.Type, e.Priority, e.Data, HashEventPayload(e.Type, e.Data))
		pos += 5
	}
	query := `INSERT INTO session_events (session_id, type, priority, data, data_hash) VALUES ` + strings.Join(placeholders, ", ") //nolint:gosec // operator-controlled column list, value placeholders

	accepted := 0
	err := inTx(ctx, r.queryer, func(tx *sql.Tx) error {
		if err := verifySessionInNamespace(ctx, tx, namespace, sessionID); err != nil {
			return err
		}
		res, err := tx.ExecContext(ctx, query, args...)
		if err != nil {
			var pgErr *pgconn.PgError
			if errors.As(err, &pgErr) && pgErr.Code == "23503" {
				return ErrSessionNotFound
			}
			return fmt.Errorf("%w: insert %d events for session %d in %q: %w",
				ErrStorageBackend, len(events), sessionID, namespace, err)
		}
		n, _ := res.RowsAffected()
		accepted = int(n)
		return nil
	})
	return accepted, err
}

func (r *eventsRepo) Read(ctx context.Context, namespace string, sessionID int64, filter EventFilter) ([]Event, error) {
	if namespace == "" {
		return nil, ErrNamespaceRequired
	}
	if filter.Limit < 0 {
		return nil, ErrInvalidLimit
	}
	if filter.MinPriority != 0 && (filter.MinPriority < 1 || filter.MinPriority > 4) {
		return nil, ErrInvalidPriority
	}
	if err := verifySessionInNamespace(ctx, r.queryer, namespace, sessionID); err != nil {
		return nil, err
	}

	limit := filter.Limit
	if limit == 0 {
		limit = defaultReadEventsLimit
	}

	clauses := []string{"session_id = $1"}
	args := []any{sessionID}
	if len(filter.Types) > 0 {
		args = append(args, filter.Types)
		clauses = append(clauses, fmt.Sprintf("type = ANY($%d)", len(args)))
	}
	if filter.MinPriority > 0 {
		args = append(args, filter.MinPriority)
		clauses = append(clauses, fmt.Sprintf("priority <= $%d", len(args)))
	}
	args = append(args, limit)
	query := `SELECT id, session_id, type, priority, data, created_at
	          FROM session_events
	          WHERE ` + strings.Join(clauses, " AND ") + `
	          ORDER BY priority ASC, created_at DESC
	          LIMIT $` + fmt.Sprint(len(args)) //nolint:gosec // operator-controlled clauses, value placeholders

	rows, err := r.queryer.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("%w: read events for session %d in %q: %w",
			ErrStorageBackend, sessionID, namespace, err)
	}
	defer func() { _ = rows.Close() }()

	out := make([]Event, 0, limit)
	for rows.Next() {
		var ev Event
		if err := rows.Scan(&ev.ID, &ev.SessionID, &ev.Type, &ev.Priority, &ev.Data, &ev.CreatedAt); err != nil {
			return nil, fmt.Errorf("%w: scan event for session %d in %q: %w",
				ErrStorageBackend, sessionID, namespace, err)
		}
		out = append(out, ev)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("%w: iterate events for session %d in %q: %w",
			ErrStorageBackend, sessionID, namespace, err)
	}
	return out, nil
}

// HashEventPayload is the canonical event-identity hash used for dedup and
// row-content fingerprinting. Length-prefixes both fields so that distinct
// (type, data) pairs that happen to share a concatenation cannot collide
// (e.g. type "ab" + data "c" hashes differently from type "a" + data "bc").
func HashEventPayload(eventType string, data []byte) []byte {
	h := sha256.New()
	var lenBuf [8]byte
	binary.BigEndian.PutUint64(lenBuf[:], uint64(len(eventType)))
	h.Write(lenBuf[:])
	h.Write([]byte(eventType))
	binary.BigEndian.PutUint64(lenBuf[:], uint64(len(data)))
	h.Write(lenBuf[:])
	h.Write(data)
	return h.Sum(nil)
}

func verifySessionInNamespace(ctx context.Context, q queryer, namespace string, sessionID int64) error {
	var exists bool
	if err := q.QueryRowContext(ctx,
		`SELECT EXISTS(SELECT 1 FROM sessions WHERE id = $1 AND namespace = $2)`,
		sessionID, namespace,
	).Scan(&exists); err != nil {
		return fmt.Errorf("%w: verify session %d in %q: %w", ErrStorageBackend, sessionID, namespace, err)
	}
	if !exists {
		return ErrSessionNotFound
	}
	return nil
}
