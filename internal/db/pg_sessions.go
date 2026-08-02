// Copyright 2026 The CALM Authors
// SPDX-License-Identifier: Apache-2.0

package db

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	logging "github.com/one-harsh/context-logging"
)

type sessionRepo struct {
	queryer queryer
	logger  *logging.Logger
}

func (r *sessionRepo) Insert(ctx context.Context, sess *Session) error {
	if sess.Namespace == "" {
		return ErrNamespaceRequired
	}
	if len(sess.SessionTokenHash) == 0 {
		return ErrSessionTokenHashRequired
	}
	if sess.TTLMinutes <= 0 {
		return ErrInvalidTTL
	}
	if sess.Client == "" {
		sess.Client = DefaultClient
	}

	// last_activity is stamped from the app clock (not the column's NOW()
	// default) so it shares a clock with Touch; otherwise GREATEST would mix
	// the DB clock at create with the app clock on Touch and last_activity
	// could appear to move backward under DB/app clock skew.
	if err := r.queryer.QueryRowContext(
		ctx,
		`INSERT INTO sessions (namespace, session_token_hash, client, ttl_minutes, last_activity)
		 VALUES ($1, $2, $3, $4, $5)
		 RETURNING id, created_at, last_activity, expires_at`,
		sess.Namespace, sess.SessionTokenHash, sess.Client, sess.TTLMinutes, time.Now().UTC(),
	).Scan(&sess.ID, &sess.CreatedAt, &sess.LastActivity, &sess.ExpiresAt); err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) {
			switch pgErr.Code {
			case "23505":
				return ErrSessionExists
			case "23503":
				// foreign_key_violation on (namespace, client) → clients FK.
				return ErrClientNotFound
			}
		}
		return fmt.Errorf("%w: insert session in %q: %w", ErrStorageBackend, sess.Namespace, err)
	}
	return nil
}

func (r *sessionRepo) InsertLabels(ctx context.Context, sessionID int64, labels map[string]string) error {
	if len(labels) == 0 {
		return nil
	}
	placeholders := make([]string, 0, len(labels))
	args := make([]any, 0, len(labels)*3)
	i := 1
	for k, v := range labels {
		placeholders = append(placeholders, fmt.Sprintf("($%d, $%d, $%d)", i, i+1, i+2))
		args = append(args, sessionID, k, v)
		i += 3
	}
	query := `INSERT INTO session_labels (session_id, key, value) VALUES ` + strings.Join(placeholders, ", ") //nolint:gosec
	if _, err := r.queryer.ExecContext(ctx, query, args...); err != nil {
		return fmt.Errorf("%w: insert labels for session %d: %w", ErrStorageBackend, sessionID, err)
	}
	return nil
}

func (r *sessionRepo) Get(ctx context.Context, namespace string, sessionTokenHash []byte) (Session, error) {
	if namespace == "" {
		return Session{}, ErrNamespaceRequired
	}
	if len(sessionTokenHash) == 0 {
		return Session{}, ErrSessionTokenHashRequired
	}

	var sess Session
	var labelsJSON []byte
	err := r.queryer.QueryRowContext(
		ctx, `
		SELECT
			s.id, s.namespace, s.client, s.created_at, s.last_activity, s.expires_at, s.ttl_minutes,
			COALESCE(
				jsonb_object_agg(sl.key, sl.value) FILTER (WHERE sl.key IS NOT NULL),
				'{}'::jsonb
			) AS labels
		FROM sessions s
		LEFT JOIN session_labels sl ON sl.session_id = s.id
		WHERE s.namespace = $1 AND s.session_token_hash = $2
		GROUP BY s.id`,
		namespace, sessionTokenHash,
	).Scan(
		&sess.ID, &sess.Namespace, &sess.Client,
		&sess.CreatedAt, &sess.LastActivity, &sess.ExpiresAt, &sess.TTLMinutes,
		&labelsJSON,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return Session{}, ErrSessionNotFound
	}
	if err != nil {
		return Session{}, fmt.Errorf("%w: get session in %q: %w", ErrStorageBackend, namespace, err)
	}
	if len(labelsJSON) > 0 {
		var labels map[string]string
		if err := json.Unmarshal(labelsJSON, &labels); err != nil {
			return Session{}, fmt.Errorf("%w: decode labels for session %d: %w", ErrStorageBackend, sess.ID, err)
		}
		if len(labels) > 0 {
			sess.Labels = labels
		}
	}
	return sess, nil
}

func (r *sessionRepo) Touch(ctx context.Context, namespace string, sessionTokenHash []byte, lastActivity time.Time) error {
	if namespace == "" {
		return ErrNamespaceRequired
	}
	if len(sessionTokenHash) == 0 {
		return ErrSessionTokenHashRequired
	}

	result, err := r.queryer.ExecContext(
		ctx,
		`UPDATE sessions SET last_activity = GREATEST(last_activity, $3)
		   WHERE namespace = $1 AND session_token_hash = $2`,
		namespace, sessionTokenHash, lastActivity,
	)
	if err != nil {
		return fmt.Errorf("%w: touch session in %q: %w", ErrStorageBackend, namespace, err)
	}
	n, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("%w: rows-affected for touch in %q: %w", ErrStorageBackend, namespace, err)
	}
	if n == 0 {
		return ErrSessionNotFound
	}
	return nil
}

func (r *sessionRepo) List(ctx context.Context, filter ListSessionsFilter) ([]ManagedSession, error) {
	if filter.Namespace == "" {
		return nil, ErrNamespaceRequired
	}

	whereClause, args := buildSessionFilterWhere(filter, "s")
	queryHead := `
		SELECT
			s.id, s.namespace, s.client, s.created_at, s.last_activity, s.expires_at, s.ttl_minutes,
			COALESCE(
				jsonb_object_agg(sl.key, sl.value) FILTER (WHERE sl.key IS NOT NULL),
				'{}'::jsonb
			) AS labels,
			(SELECT COUNT(*) FROM session_events WHERE session_id = s.id) AS event_count
		FROM sessions s
		LEFT JOIN session_labels sl ON sl.session_id = s.id
		WHERE `
	queryTail := `
		GROUP BY s.id
		ORDER BY s.last_activity DESC`
	query := queryHead + whereClause + queryTail //nolint:gosec

	rows, err := r.queryer.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("%w: list sessions in %q: %w", ErrStorageBackend, filter.Namespace, err)
	}
	defer func() { _ = rows.Close() }()

	out := make([]ManagedSession, 0)
	for rows.Next() {
		var ms ManagedSession
		var labelsJSON []byte
		if err := rows.Scan(
			&ms.ID, &ms.Namespace, &ms.Client,
			&ms.CreatedAt, &ms.LastActivity, &ms.ExpiresAt, &ms.TTLMinutes,
			&labelsJSON, &ms.EventCount,
		); err != nil {
			return nil, fmt.Errorf("%w: scan session row in %q: %w", ErrStorageBackend, filter.Namespace, err)
		}
		if len(labelsJSON) > 0 {
			var labels map[string]string
			if err := json.Unmarshal(labelsJSON, &labels); err != nil {
				return nil, fmt.Errorf("%w: decode labels in %q: %w", ErrStorageBackend, filter.Namespace, err)
			}
			if len(labels) > 0 {
				ms.Labels = labels
			}
		}
		out = append(out, ms)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("%w: iterate session rows in %q: %w", ErrStorageBackend, filter.Namespace, err)
	}
	return out, nil
}

func (r *sessionRepo) Count(ctx context.Context, filter ListSessionsFilter) (int, error) {
	if filter.Namespace == "" {
		return 0, ErrNamespaceRequired
	}
	whereClause, args := buildSessionFilterWhere(filter, "s")
	query := `SELECT COUNT(*) FROM sessions s WHERE ` + whereClause //nolint:gosec
	var count int
	if err := r.queryer.QueryRowContext(ctx, query, args...).Scan(&count); err != nil {
		return 0, fmt.Errorf("%w: count sessions in %q: %w", ErrStorageBackend, filter.Namespace, err)
	}
	return count, nil
}

// LockByTokenHash locks the session row (FOR UPDATE) so a cascade count and
// delete composed in the same transaction observe a stable child-row set.
func (r *sessionRepo) LockByTokenHash(ctx context.Context, namespace string, sessionTokenHash []byte) (id int64, client string, err error) {
	if namespace == "" {
		return 0, "", ErrNamespaceRequired
	}
	if len(sessionTokenHash) == 0 {
		return 0, "", ErrSessionTokenHashRequired
	}
	err = r.queryer.QueryRowContext(
		ctx,
		`SELECT id, client FROM sessions
		   WHERE namespace = $1 AND session_token_hash = $2 FOR UPDATE`,
		namespace, sessionTokenHash,
	).Scan(&id, &client)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, "", ErrSessionNotFound
	}
	if err != nil {
		return 0, "", fmt.Errorf("%w: lock session in %q: %w", ErrStorageBackend, namespace, err)
	}
	return id, client, nil
}

// LockByID locks a session by surrogate id; namespace pairing is defense-in-depth
// (the surrogate id alone is unique).
func (r *sessionRepo) LockByID(ctx context.Context, namespace string, sessionID int64) (client string, lastActivity time.Time, err error) {
	if namespace == "" {
		return "", time.Time{}, ErrNamespaceRequired
	}
	err = r.queryer.QueryRowContext(
		ctx,
		`SELECT client, last_activity FROM sessions WHERE id = $1 AND namespace = $2 FOR UPDATE`,
		sessionID, namespace,
	).Scan(&client, &lastActivity)
	if errors.Is(err, sql.ErrNoRows) {
		return "", time.Time{}, ErrSessionNotFound
	}
	if err != nil {
		return "", time.Time{}, fmt.Errorf("%w: lock session %d in %q: %w", ErrStorageBackend, sessionID, namespace, err)
	}
	return client, lastActivity, nil
}

// LockAllByFilter locks the matching session rows and returns their ids. ORDER BY
// id gives a deterministic lock order across concurrent callers, avoiding
// cross-statement deadlocks on overlapping session sets.
func (r *sessionRepo) LockAllByFilter(ctx context.Context, filter ListSessionsFilter) ([]int64, error) {
	if filter.Namespace == "" {
		return nil, ErrNamespaceRequired
	}
	whereClause, args := buildSessionFilterWhere(filter, "")
	lockQuery := `SELECT id FROM sessions WHERE ` + whereClause + ` ORDER BY id FOR UPDATE` //nolint:gosec
	rows, err := r.queryer.QueryContext(ctx, lockQuery, args...)
	if err != nil {
		return nil, fmt.Errorf("%w: lock sessions in %q: %w", ErrStorageBackend, filter.Namespace, err)
	}
	defer func() { _ = rows.Close() }()
	ids := make([]int64, 0)
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("%w: scan locked session row in %q: %w", ErrStorageBackend, filter.Namespace, err)
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("%w: iterate locked session rows in %q: %w", ErrStorageBackend, filter.Namespace, err)
	}
	return ids, nil
}

// CascadeCounts returns the child-row counts a delete of this session will cascade.
func (r *sessionRepo) CascadeCounts(ctx context.Context, sessionID int64) (CascadeCounts, error) {
	var c CascadeCounts
	err := r.queryer.QueryRowContext(
		ctx, `
		SELECT
			(SELECT COUNT(*) FROM sources        WHERE session_id = $1),
			(SELECT COUNT(*) FROM chunks         WHERE source_id IN
				(SELECT id FROM sources WHERE session_id = $1)),
			(SELECT COUNT(*) FROM session_events WHERE session_id = $1),
			(SELECT COUNT(*) FROM session_labels WHERE session_id = $1)`,
		sessionID,
	).Scan(&c.Sources, &c.Chunks, &c.Events, &c.Labels)
	if err != nil {
		return CascadeCounts{}, fmt.Errorf("%w: count cascade for session %d: %w", ErrStorageBackend, sessionID, err)
	}
	return c, nil
}

// CascadeCountsForIDs is the bulk variant used when deleting many sessions.
func (r *sessionRepo) CascadeCountsForIDs(ctx context.Context, ids []int64) (CascadeCounts, error) {
	var c CascadeCounts
	err := r.queryer.QueryRowContext(
		ctx, `
		SELECT
			(SELECT COUNT(*) FROM sources        WHERE session_id = ANY($1)),
			(SELECT COUNT(*) FROM chunks         WHERE source_id IN
				(SELECT id FROM sources WHERE session_id = ANY($1))),
			(SELECT COUNT(*) FROM session_events WHERE session_id = ANY($1)),
			(SELECT COUNT(*) FROM session_labels WHERE session_id = ANY($1))`,
		ids,
	).Scan(&c.Sources, &c.Chunks, &c.Events, &c.Labels)
	if err != nil {
		return CascadeCounts{}, fmt.Errorf("%w: count cascade for sessions: %w", ErrStorageBackend, err)
	}
	return c, nil
}

// DeleteByIDRow removes the session row; children go via ON DELETE CASCADE.
func (r *sessionRepo) DeleteByIDRow(ctx context.Context, sessionID int64) error {
	if _, err := r.queryer.ExecContext(ctx, `DELETE FROM sessions WHERE id = $1`, sessionID); err != nil {
		return fmt.Errorf("%w: delete session %d: %w", ErrStorageBackend, sessionID, err)
	}
	return nil
}

// DeleteRows removes many session rows; children go via ON DELETE CASCADE.
func (r *sessionRepo) DeleteRows(ctx context.Context, ids []int64) error {
	if len(ids) == 0 {
		return nil
	}
	if _, err := r.queryer.ExecContext(ctx, `DELETE FROM sessions WHERE id = ANY($1)`, ids); err != nil {
		return fmt.Errorf("%w: delete sessions: %w", ErrStorageBackend, err)
	}
	return nil
}

func (r *sessionRepo) ScanExpired(ctx context.Context, now time.Time) ([]SessionRef, error) {
	rows, err := r.queryer.QueryContext(
		ctx, `
		SELECT id, namespace
		FROM sessions
		WHERE expires_at < $1
		ORDER BY expires_at ASC`,
		now,
	)
	if err != nil {
		return nil, fmt.Errorf("%w: scan expired sessions: %w", ErrStorageBackend, err)
	}
	defer func() { _ = rows.Close() }()

	out := make([]SessionRef, 0)
	for rows.Next() {
		var ref SessionRef
		if err := rows.Scan(&ref.ID, &ref.Namespace); err != nil {
			return nil, fmt.Errorf("%w: scan expired session row: %w", ErrStorageBackend, err)
		}
		out = append(out, ref)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("%w: iterate expired session rows: %w", ErrStorageBackend, err)
	}
	return out, nil
}

func buildSessionFilterWhere(filter ListSessionsFilter, alias string) (string, []any) {
	prefix := ""
	if alias != "" {
		prefix = alias + "."
	}

	clauses := make([]string, 0, 3)
	args := make([]any, 0, 2+2*len(filter.Labels))

	args = append(args, filter.Namespace)
	clauses = append(clauses, fmt.Sprintf("%snamespace = $%d", prefix, len(args)))

	if filter.Client != "" {
		args = append(args, filter.Client)
		clauses = append(clauses, fmt.Sprintf("%sclient = $%d", prefix, len(args)))
	}

	if len(filter.Labels) > 0 {
		pairs := make([]string, 0, len(filter.Labels))
		for k, v := range filter.Labels {
			args = append(args, k)
			kIdx := len(args)
			args = append(args, v)
			vIdx := len(args)
			pairs = append(pairs, fmt.Sprintf("(key = $%d AND value = $%d)", kIdx, vIdx))
		}
		clauses = append(clauses, fmt.Sprintf(
			"%sid IN ("+
				"SELECT session_id FROM session_labels "+
				"WHERE (%s) "+
				"GROUP BY session_id "+
				"HAVING COUNT(DISTINCT key) = %d)",
			prefix, strings.Join(pairs, " OR "), len(filter.Labels),
		))
	}

	return strings.Join(clauses, " AND "), args
}
