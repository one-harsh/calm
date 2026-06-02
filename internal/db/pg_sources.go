// Copyright 2026 The CALM Authors
// SPDX-License-Identifier: Apache-2.0

package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5/pgconn"
	logging "github.com/one-harsh/context-logging"
)

type sourcesRepo struct {
	queryer queryer
	logger  *logging.Logger
}

func (r *sourcesRepo) Index(ctx context.Context, namespace string, in IndexInput) error {
	if namespace == "" {
		return ErrNamespaceRequired
	}
	if in.Source == "" {
		return ErrSourceRequired
	}
	return inTx(ctx, r.queryer, func(tx *sql.Tx) error {
		if err := verifySessionInNamespace(ctx, tx, namespace, in.SessionID); err != nil {
			return err
		}
		// idempotent-indexing invariant: re-ingesting a source replaces its prior content.
		var sourceID int64
		err := tx.QueryRowContext(ctx,
			`INSERT INTO sources (session_id, label) VALUES ($1, $2)
			 ON CONFLICT (session_id, label) DO UPDATE SET indexed_at = now()
			 RETURNING id`,
			in.SessionID, in.Source,
		).Scan(&sourceID)
		if err != nil {
			var pgErr *pgconn.PgError
			if errors.As(err, &pgErr) && pgErr.Code == "23503" {
				return ErrSessionNotFound
			}
			return fmt.Errorf("%w: upsert source %q for session %d in %q: %w",
				ErrStorageBackend, in.Source, in.SessionID, namespace, err)
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM chunks WHERE source_id = $1`, sourceID); err != nil {
			return fmt.Errorf("%w: clear chunks for source %d: %w", ErrStorageBackend, sourceID, err)
		}
		// Empty content is valid: the upsert+clear above leaves the source with no chunks
		if len(in.Chunks) == 0 {
			return nil
		}

		placeholders := make([]string, 0, len(in.Chunks))
		args := make([]any, 0, len(in.Chunks)*4)
		pos := 1
		for _, c := range in.Chunks {
			placeholders = append(placeholders,
				fmt.Sprintf("($%d, $%d, $%d, $%d)", pos, pos+1, pos+2, pos+3))
			args = append(args, sourceID, c.Title, c.Content, c.ContentType)
			pos += 4
		}
		query := `INSERT INTO chunks (source_id, title, content, content_type) VALUES ` + strings.Join(placeholders, ", ") //nolint:gosec // operator-controlled column list, value placeholders
		if _, err := tx.ExecContext(ctx, query, args...); err != nil {
			return fmt.Errorf("%w: insert %d chunks for source %d: %w",
				ErrStorageBackend, len(in.Chunks), sourceID, err)
		}
		return nil
	})
}

func (r *sourcesRepo) List(ctx context.Context, namespace string, sessionID int64) ([]SourceSummary, error) {
	if namespace == "" {
		return nil, ErrNamespaceRequired
	}
	if err := verifySessionInNamespace(ctx, r.queryer, namespace, sessionID); err != nil {
		return nil, err
	}

	const query = `SELECT s.label, s.indexed_at, COUNT(c.id)
	               FROM sources s LEFT JOIN chunks c ON c.source_id = s.id
	               WHERE s.session_id = $1
	               GROUP BY s.id, s.label, s.indexed_at
	               ORDER BY s.indexed_at DESC`

	rows, err := r.queryer.QueryContext(ctx, query, sessionID)
	if err != nil {
		return nil, fmt.Errorf("%w: list sources for session %d in %q: %w",
			ErrStorageBackend, sessionID, namespace, err)
	}
	defer func() { _ = rows.Close() }()

	out := []SourceSummary{}
	for rows.Next() {
		var s SourceSummary
		if err := rows.Scan(&s.Label, &s.IndexedAt, &s.Chunks); err != nil {
			return nil, fmt.Errorf("%w: scan source for session %d in %q: %w",
				ErrStorageBackend, sessionID, namespace, err)
		}
		out = append(out, s)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("%w: iterate sources for session %d in %q: %w",
			ErrStorageBackend, sessionID, namespace, err)
	}
	return out, nil
}
