// Copyright 2026 The CALM Authors
// SPDX-License-Identifier: Apache-2.0

package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	logging "github.com/one-harsh/context-logging"
)

type clientRepo struct {
	queryer queryer
	logger  *logging.Logger
}

func (r *clientRepo) Register(ctx context.Context, namespace, name string) (bool, error) {
	if namespace == "" {
		return false, ErrNamespaceRequired
	}
	if name == "" {
		return false, ErrClientNameRequired
	}
	result, err := r.queryer.ExecContext(
		ctx,
		`INSERT INTO clients (namespace, name) VALUES ($1, $2) ON CONFLICT (namespace, name) DO NOTHING`,
		namespace, name,
	)
	if err != nil {
		return false, fmt.Errorf("%w: register client %q/%q: %w", ErrStorageBackend, namespace, name, err)
	}
	n, err := result.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("%w: rows-affected for register %q/%q: %w", ErrStorageBackend, namespace, name, err)
	}
	return n > 0, nil
}

func (r *clientRepo) RegisterWithCredential(ctx context.Context, namespace, name string, tokenHash []byte) error {
	if namespace == "" {
		return ErrNamespaceRequired
	}
	if name == "" {
		return ErrClientNameRequired
	}
	if len(tokenHash) == 0 {
		return ErrInvalidClientCredential
	}
	_, err := r.queryer.ExecContext(
		ctx,
		`INSERT INTO clients (namespace, name, client_token_hash, token_issued_at)
		 VALUES ($1, $2, $3, now())`,
		namespace, name, tokenHash,
	)
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			return ErrClientExists
		}
		return fmt.Errorf("%w: register credentialed client %q/%q: %w", ErrStorageBackend, namespace, name, err)
	}
	return nil
}

func (r *clientRepo) RotateCredential(ctx context.Context, namespace, name string, newHash []byte) error {
	if namespace == "" {
		return ErrNamespaceRequired
	}
	if name == "" {
		return ErrClientNameRequired
	}
	if len(newHash) == 0 {
		return ErrInvalidClientCredential
	}
	result, err := r.queryer.ExecContext(
		ctx,
		`UPDATE clients SET client_token_hash = $3, token_rotated_at = now()
		  WHERE namespace = $1 AND name = $2 AND client_token_hash IS NOT NULL`,
		namespace, name, newHash,
	)
	if err != nil {
		return fmt.Errorf("%w: rotate client credential %q/%q: %w", ErrStorageBackend, namespace, name, err)
	}
	n, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("%w: rows-affected for rotate %q/%q: %w", ErrStorageBackend, namespace, name, err)
	}
	if n == 0 {
		// Either the client doesn't exist or it was registered without a
		// credential. Both surface as not-found from the caller's
		// perspective — there's no token-bearing row to rotate.
		return ErrClientNotFound
	}
	return nil
}

func (r *clientRepo) LookupByToken(ctx context.Context, namespace string, tokenHash []byte) (string, error) {
	if namespace == "" {
		return "", ErrNamespaceRequired
	}
	if len(tokenHash) == 0 {
		return "", ErrInvalidClientCredential
	}
	var name string
	err := r.queryer.QueryRowContext(
		ctx,
		`SELECT name FROM clients WHERE namespace = $1 AND client_token_hash = $2`,
		namespace, tokenHash,
	).Scan(&name)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrInvalidClientCredential
	}
	if err != nil {
		return "", fmt.Errorf("%w: lookup client by token in %q: %w", ErrStorageBackend, namespace, err)
	}
	return name, nil
}

func (r *clientRepo) List(ctx context.Context, namespace string) ([]ClientSummary, error) {
	if namespace == "" {
		return nil, ErrNamespaceRequired
	}
	rows, err := r.queryer.QueryContext(
		ctx,
		`SELECT c.name, COUNT(s.id), MAX(s.last_activity)
		   FROM clients c
		   LEFT JOIN sessions s
		     ON s.namespace = c.namespace AND s.client = c.name
		  WHERE c.namespace = $1
		  GROUP BY c.name
		  ORDER BY c.name ASC`,
		namespace,
	)
	if err != nil {
		return nil, fmt.Errorf("%w: list clients in %q: %w", ErrStorageBackend, namespace, err)
	}
	defer func() { _ = rows.Close() }()

	out := make([]ClientSummary, 0)
	for rows.Next() {
		var sum ClientSummary
		var last sql.NullTime
		if err := rows.Scan(&sum.Name, &sum.SessionCount, &last); err != nil {
			return nil, fmt.Errorf("%w: scan client row in %q: %w", ErrStorageBackend, namespace, err)
		}
		if last.Valid {
			t := last.Time
			sum.LastActivity = &t
		}
		out = append(out, sum)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("%w: iterate client rows in %q: %w", ErrStorageBackend, namespace, err)
	}
	return out, nil
}

func (r *clientRepo) CountSessions(ctx context.Context, namespace, name string) (int, error) {
	if namespace == "" {
		return 0, ErrNamespaceRequired
	}
	if name == "" {
		return 0, ErrClientNameRequired
	}
	var exists bool
	var count int
	err := r.queryer.QueryRowContext(
		ctx,
		`SELECT EXISTS (SELECT 1 FROM clients WHERE namespace = $1 AND name = $2),
		        (SELECT COUNT(*) FROM sessions WHERE namespace = $1 AND client = $2)`,
		namespace, name,
	).Scan(&exists, &count)
	if err != nil {
		return 0, fmt.Errorf("%w: count sessions for %q/%q: %w", ErrStorageBackend, namespace, name, err)
	}
	if !exists {
		return 0, ErrClientNotFound
	}
	return count, nil
}

// LockByName locks the client row (FOR UPDATE) so a cascade count and delete
// composed in the same transaction observe a stable child-row set.
func (r *clientRepo) LockByName(ctx context.Context, namespace, name string) error {
	if namespace == "" {
		return ErrNamespaceRequired
	}
	if name == "" {
		return ErrClientNameRequired
	}
	var one int
	err := r.queryer.QueryRowContext(
		ctx,
		`SELECT 1 FROM clients WHERE namespace = $1 AND name = $2 FOR UPDATE`,
		namespace, name,
	).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrClientNotFound
	}
	if err != nil {
		return fmt.Errorf("%w: lock client %q/%q: %w", ErrStorageBackend, namespace, name, err)
	}
	return nil
}

// CascadeCountsForClient returns the sessions (and their descendants) that a
// delete of this client will cascade.
func (r *clientRepo) CascadeCountsForClient(ctx context.Context, namespace, name string) (deletedSessions int, c CascadeCounts, err error) {
	err = r.queryer.QueryRowContext(
		ctx,
		`WITH target_sessions AS (
		   SELECT id FROM sessions WHERE namespace = $1 AND client = $2
		 )
		 SELECT
		   (SELECT COUNT(*) FROM target_sessions),
		   (SELECT COUNT(*) FROM sources
		      WHERE session_id IN (SELECT id FROM target_sessions)),
		   (SELECT COUNT(*) FROM chunks WHERE source_id IN
		      (SELECT id FROM sources
		         WHERE session_id IN (SELECT id FROM target_sessions))),
		   (SELECT COUNT(*) FROM session_events
		      WHERE session_id IN (SELECT id FROM target_sessions)),
		   (SELECT COUNT(*) FROM session_labels
		      WHERE session_id IN (SELECT id FROM target_sessions))`,
		namespace, name,
	).Scan(&deletedSessions, &c.Sources, &c.Chunks, &c.Events, &c.Labels)
	if err != nil {
		return 0, CascadeCounts{}, fmt.Errorf("%w: count cascade for %q/%q: %w", ErrStorageBackend, namespace, name, err)
	}
	return deletedSessions, c, nil
}

// DeleteRow removes the client row; sessions (and their children) go via ON DELETE CASCADE.
func (r *clientRepo) DeleteRow(ctx context.Context, namespace, name string) error {
	if _, err := r.queryer.ExecContext(
		ctx,
		`DELETE FROM clients WHERE namespace = $1 AND name = $2`,
		namespace, name,
	); err != nil {
		return fmt.Errorf("%w: delete client %q/%q: %w", ErrStorageBackend, namespace, name, err)
	}
	return nil
}

// BumpActivity advances last_activity_at to the newer of its current value and ts.
// ts is sampled on the caller's clock, so a bump carrying an older ts can land
// after a newer one; GREATEST keeps the column monotonic under that reordering.
func (r *clientRepo) BumpActivity(ctx context.Context, namespace, name string, ts time.Time) error {
	if _, err := r.queryer.ExecContext(
		ctx,
		`UPDATE clients SET last_activity_at = GREATEST(last_activity_at, $3)
		 WHERE namespace = $1 AND name = $2`,
		namespace, name, ts,
	); err != nil {
		return fmt.Errorf("%w: bump clients.last_activity_at for %q/%q: %w", ErrStorageBackend, namespace, name, err)
	}
	return nil
}
