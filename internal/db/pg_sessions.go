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

// Create inserts the session row + any declared labels atomically. The
// (namespace, client) row in clients must exist; a missing client surfaces
// as ErrClientNotFound (translated from Postgres FK violation 23503).
// Clients are first-class registered entities — POST /v1/clients/{name} for
// custom names, SeedDefaults for `default`.
func (r *sessionRepo) Create(ctx context.Context, sess *Session) error {
	if sess.Namespace == "" {
		return ErrNamespaceRequired
	}
	if sess.ID == "" {
		return ErrSessionIDRequired
	}
	if sess.TTLMinutes <= 0 {
		return ErrInvalidTTL
	}
	if sess.Client == "" {
		sess.Client = DefaultClient
	}

	return inTx(ctx, r.queryer, func(tx *sql.Tx) error {
		if err := tx.QueryRowContext(ctx,
			`INSERT INTO sessions (session_id, namespace, client, ttl_minutes)
			 VALUES ($1, $2, $3, $4)
			 RETURNING created_at, last_activity, expires_at`,
			sess.ID, sess.Namespace, sess.Client, sess.TTLMinutes,
		).Scan(&sess.CreatedAt, &sess.LastActivity, &sess.ExpiresAt); err != nil {
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
			return fmt.Errorf("%w: insert session %q/%q: %w", ErrStorageBackend, sess.Namespace, sess.ID, err)
		}

		if len(sess.Labels) > 0 {
			placeholders := make([]string, 0, len(sess.Labels))
			args := make([]any, 0, len(sess.Labels)*4)
			i := 1
			for k, v := range sess.Labels {
				placeholders = append(placeholders, fmt.Sprintf("($%d, $%d, $%d, $%d)", i, i+1, i+2, i+3))
				args = append(args, sess.Namespace, sess.ID, k, v)
				i += 4
			}
			query := `INSERT INTO session_labels (namespace, session_id, key, value) VALUES ` + strings.Join(placeholders, ", ") //nolint:gosec
			if _, err := tx.ExecContext(ctx, query, args...); err != nil {
				return fmt.Errorf("%w: insert labels for session %q/%q: %w", ErrStorageBackend, sess.Namespace, sess.ID, err)
			}
		}
		return nil
	})
}

func (r *sessionRepo) Get(ctx context.Context, namespace, id string) (Session, error) {
	if namespace == "" {
		return Session{}, ErrNamespaceRequired
	}
	if id == "" {
		return Session{}, ErrSessionIDRequired
	}

	var sess Session
	var labelsJSON []byte
	err := r.queryer.QueryRowContext(ctx, `
		SELECT
			s.session_id, s.namespace, s.client, s.created_at, s.last_activity, s.expires_at, s.ttl_minutes,
			COALESCE(
				jsonb_object_agg(sl.key, sl.value) FILTER (WHERE sl.key IS NOT NULL),
				'{}'::jsonb
			) AS labels
		FROM sessions s
		LEFT JOIN session_labels sl
			ON sl.namespace = s.namespace AND sl.session_id = s.session_id
		WHERE s.namespace = $1 AND s.session_id = $2
		GROUP BY s.session_id, s.namespace, s.client, s.created_at, s.last_activity, s.expires_at, s.ttl_minutes`,
		namespace, id,
	).Scan(
		&sess.ID, &sess.Namespace, &sess.Client,
		&sess.CreatedAt, &sess.LastActivity, &sess.ExpiresAt, &sess.TTLMinutes,
		&labelsJSON,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return Session{}, ErrSessionNotFound
	}
	if err != nil {
		return Session{}, fmt.Errorf("%w: get session %q/%q: %w", ErrStorageBackend, namespace, id, err)
	}
	if len(labelsJSON) > 0 {
		var labels map[string]string
		if err := json.Unmarshal(labelsJSON, &labels); err != nil {
			return Session{}, fmt.Errorf("%w: decode labels for %q/%q: %w", ErrStorageBackend, namespace, id, err)
		}
		if len(labels) > 0 {
			sess.Labels = labels
		}
	}
	return sess, nil
}

func (r *sessionRepo) Touch(ctx context.Context, namespace, id string, lastActivity time.Time) error {
	if namespace == "" {
		return ErrNamespaceRequired
	}
	if id == "" {
		return ErrSessionIDRequired
	}

	result, err := r.queryer.ExecContext(ctx,
		`UPDATE sessions SET last_activity = GREATEST(last_activity, $3) WHERE namespace = $1 AND session_id = $2`,
		namespace, id, lastActivity,
	)
	if err != nil {
		return fmt.Errorf("%w: touch session %q/%q: %w", ErrStorageBackend, namespace, id, err)
	}
	n, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("%w: rows-affected for touch %q/%q: %w", ErrStorageBackend, namespace, id, err)
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
			s.session_id, s.namespace, s.client, s.created_at, s.last_activity, s.expires_at, s.ttl_minutes,
			COALESCE(
				jsonb_object_agg(sl.key, sl.value) FILTER (WHERE sl.key IS NOT NULL),
				'{}'::jsonb
			) AS labels,
			(SELECT COUNT(*) FROM session_events
				WHERE namespace = s.namespace AND session_id = s.session_id) AS event_count
		FROM sessions s
		LEFT JOIN session_labels sl
			ON sl.namespace = s.namespace AND sl.session_id = s.session_id
		WHERE `
	queryTail := `
		GROUP BY s.session_id, s.namespace, s.client, s.created_at, s.last_activity, s.expires_at, s.ttl_minutes
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

func (r *sessionRepo) Delete(ctx context.Context, namespace, id string) (DeleteSessionResult, error) {
	if namespace == "" {
		return DeleteSessionResult{}, ErrNamespaceRequired
	}
	if id == "" {
		return DeleteSessionResult{}, ErrSessionIDRequired
	}

	result := DeleteSessionResult{SessionID: id}
	err := inTx(ctx, r.queryer, func(tx *sql.Tx) error {
		// FOR UPDATE conflicts with FOR KEY SHARE that FK enforcement takes
		// on concurrent INSERT INTO children — those inserts block until our
		// COMMIT and then fail FK. Count and cascade see the same row set.
		var client string
		err := tx.QueryRowContext(ctx,
			`SELECT client FROM sessions
			 WHERE namespace = $1 AND session_id = $2 FOR UPDATE`,
			namespace, id,
		).Scan(&client)
		if errors.Is(err, sql.ErrNoRows) {
			return ErrSessionNotFound
		}
		if err != nil {
			return fmt.Errorf("%w: lock session %q/%q: %w", ErrStorageBackend, namespace, id, err)
		}
		r.logger.WithContext(ctx).Debug("delete session: lock acquired")

		err = tx.QueryRowContext(ctx, `
			SELECT
				(SELECT COUNT(*) FROM sources        WHERE namespace = $1 AND session_id = $2),
				(SELECT COUNT(*) FROM chunks         WHERE source_id IN
					(SELECT id FROM sources WHERE namespace = $1 AND session_id = $2)),
				(SELECT COUNT(*) FROM session_events WHERE namespace = $1 AND session_id = $2),
				(SELECT COUNT(*) FROM session_labels WHERE namespace = $1 AND session_id = $2)`,
			namespace, id,
		).Scan(
			&result.Cascaded.Sources,
			&result.Cascaded.Chunks,
			&result.Cascaded.Events,
			&result.Cascaded.Labels,
		)
		if err != nil {
			return fmt.Errorf("%w: count cascade for %q/%q: %w", ErrStorageBackend, namespace, id, err)
		}
		r.logger.WithContext(ctx).Debug("delete session: cascade computed",
			logging.IntField("sources", result.Cascaded.Sources),
			logging.IntField("chunks", result.Cascaded.Chunks),
			logging.IntField("events", result.Cascaded.Events),
			logging.IntField("labels", result.Cascaded.Labels),
		)

		// GREATEST guards against tx-start-order racing commit-order: NOW() returns
		// the transaction's start timestamp, so a delete that started earlier but
		// reaches this UPDATE later could otherwise overwrite a sibling session's
		// more recent bump.
		if _, err := tx.ExecContext(ctx,
			`UPDATE clients SET last_activity_at = GREATEST(last_activity_at, NOW())
			 WHERE namespace = $1 AND name = $2`,
			namespace, client,
		); err != nil {
			return fmt.Errorf("%w: bump clients.last_activity_at for %q/%q: %w", ErrStorageBackend, namespace, client, err)
		}

		if _, err := tx.ExecContext(ctx,
			`DELETE FROM sessions WHERE namespace = $1 AND session_id = $2`,
			namespace, id,
		); err != nil {
			return fmt.Errorf("%w: delete session %q/%q: %w", ErrStorageBackend, namespace, id, err)
		}
		r.logger.WithContext(ctx).Debug("delete session: committed")
		return nil
	})
	if err != nil {
		return DeleteSessionResult{}, err
	}
	return result, nil
}

func (r *sessionRepo) DeleteAll(ctx context.Context, filter ListSessionsFilter) (DeleteSessionsResult, error) {
	if filter.Namespace == "" {
		return DeleteSessionsResult{}, ErrNamespaceRequired
	}

	whereClause, args := buildSessionFilterWhere(filter, "")

	var result DeleteSessionsResult
	err := inTx(ctx, r.queryer, func(tx *sql.Tx) error {
		lockQuery := `SELECT session_id FROM sessions WHERE ` + whereClause + ` FOR UPDATE` //nolint:gosec
		rows, err := tx.QueryContext(ctx, lockQuery, args...)
		if err != nil {
			return fmt.Errorf("%w: lock sessions in %q: %w", ErrStorageBackend, filter.Namespace, err)
		}
		ids := make([]string, 0)
		for rows.Next() {
			var sid string
			if err := rows.Scan(&sid); err != nil {
				_ = rows.Close()
				return fmt.Errorf("%w: scan locked session row in %q: %w", ErrStorageBackend, filter.Namespace, err)
			}
			ids = append(ids, sid)
		}
		if err := rows.Err(); err != nil {
			_ = rows.Close()
			return fmt.Errorf("%w: iterate locked session rows in %q: %w", ErrStorageBackend, filter.Namespace, err)
		}
		_ = rows.Close()
		r.logger.WithContext(ctx).Debug("delete sessions: locked rows", logging.IntField("count", len(ids)))

		if len(ids) == 0 {
			return nil
		}

		result.DeletedSessions = len(ids)
		err = tx.QueryRowContext(ctx, `
			SELECT
				(SELECT COUNT(*) FROM sources        WHERE namespace = $1 AND session_id = ANY($2)),
				(SELECT COUNT(*) FROM chunks         WHERE source_id IN
					(SELECT id FROM sources WHERE namespace = $1 AND session_id = ANY($2))),
				(SELECT COUNT(*) FROM session_events WHERE namespace = $1 AND session_id = ANY($2)),
				(SELECT COUNT(*) FROM session_labels WHERE namespace = $1 AND session_id = ANY($2))`,
			filter.Namespace, ids,
		).Scan(
			&result.Cascaded.Sources,
			&result.Cascaded.Chunks,
			&result.Cascaded.Events,
			&result.Cascaded.Labels,
		)
		if err != nil {
			return fmt.Errorf("%w: count cascade for sessions in %q: %w", ErrStorageBackend, filter.Namespace, err)
		}
		r.logger.WithContext(ctx).Debug("delete sessions: cascade computed",
			logging.IntField("sources", result.Cascaded.Sources),
			logging.IntField("chunks", result.Cascaded.Chunks),
			logging.IntField("events", result.Cascaded.Events),
			logging.IntField("labels", result.Cascaded.Labels),
		)

		if _, err := tx.ExecContext(ctx,
			`DELETE FROM sessions WHERE namespace = $1 AND session_id = ANY($2)`,
			filter.Namespace, ids,
		); err != nil {
			return fmt.Errorf("%w: delete sessions in %q: %w", ErrStorageBackend, filter.Namespace, err)
		}
		r.logger.WithContext(ctx).Debug("delete sessions: committed")
		return nil
	})
	if err != nil {
		return DeleteSessionsResult{}, err
	}
	return result, nil
}

func (r *sessionRepo) ScanExpired(ctx context.Context, now time.Time) ([]SessionRef, error) {
	rows, err := r.queryer.QueryContext(ctx, `
		SELECT session_id, namespace
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
		if err := rows.Scan(&ref.SessionID, &ref.Namespace); err != nil {
			return nil, fmt.Errorf("%w: scan expired session row: %w", ErrStorageBackend, err)
		}
		out = append(out, ref)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("%w: iterate expired session rows: %w", ErrStorageBackend, err)
	}
	return out, nil
}

// buildSessionFilterWhere returns the WHERE-clause body (no leading "WHERE")
// and arg slice for List / Count / DeleteAll. The `alias` qualifies session
// columns (e.g. "s"); "" means bare. Caller validates filter.Namespace.
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
			"(%snamespace, %ssession_id) IN ("+
				"SELECT namespace, session_id FROM session_labels "+
				"WHERE namespace = $1 AND (%s) "+
				"GROUP BY namespace, session_id "+
				"HAVING COUNT(DISTINCT key) = %d)",
			prefix, prefix, strings.Join(pairs, " OR "), len(filter.Labels),
		))
	}

	return strings.Join(clauses, " AND "), args
}
