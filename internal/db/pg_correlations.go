// Copyright 2026 The CALM Authors
// SPDX-License-Identifier: Apache-2.0

package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	logging "github.com/one-harsh/context-logging"
)

type correlationsRepo struct {
	queryer queryer
	logger  *logging.Logger
}

func (r *correlationsRepo) Insert(ctx context.Context, namespace string, sessionID int64, correlationID []byte, requestType string, requestMeta []byte) error {
	if namespace == "" {
		return ErrNamespaceRequired
	}
	res, err := r.queryer.ExecContext(
		ctx,
		`INSERT INTO correlations (correlation_id, session_id, request_type, request_meta)
		 SELECT $1, $2, $3, $4
		 WHERE EXISTS (SELECT 1 FROM sessions WHERE id = $2 AND namespace = $5)`,
		correlationID, sessionID, requestType, requestMeta, namespace,
	)
	if err != nil {
		return fmt.Errorf("%w: insert correlation for session %d in %q: %w",
			ErrStorageBackend, sessionID, namespace, err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrSessionNotFound
	}
	return nil
}

func (r *correlationsRepo) GetWithLockedRow(ctx context.Context, namespace string, sessionID int64, correlationID []byte) (CorrelationRecord, error) {
	if namespace == "" {
		return CorrelationRecord{}, ErrNamespaceRequired
	}
	var rec CorrelationRecord
	var feedbackAt sql.NullTime
	err := r.queryer.QueryRowContext(
		ctx,
		`SELECT correlation_id, session_id, request_type, request_meta, outcome, feedback_received_at, created_at
		 FROM correlations
		 WHERE correlation_id = $1 AND session_id = $2
		   AND EXISTS (SELECT 1 FROM sessions WHERE id = $2 AND namespace = $3)
		 FOR UPDATE`,
		correlationID, sessionID, namespace,
	).Scan(&rec.CorrelationID, &rec.SessionID, &rec.RequestType, &rec.RequestMeta, &rec.Outcome, &feedbackAt, &rec.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return CorrelationRecord{}, ErrCorrelationNotFound
	}
	if err != nil {
		return CorrelationRecord{}, fmt.Errorf("%w: get correlation in session %d in %q: %w",
			ErrStorageBackend, sessionID, namespace, err)
	}
	rec.Namespace = namespace
	if feedbackAt.Valid {
		rec.FeedbackReceivedAt = feedbackAt.Time
	}
	return rec, nil
}

func (r *correlationsRepo) UpdateOutcome(ctx context.Context, namespace string, sessionID int64, correlationID []byte, outcome string) (CorrelationRecord, error) {
	if namespace == "" {
		return CorrelationRecord{}, ErrNamespaceRequired
	}
	var rec CorrelationRecord
	var feedbackAt sql.NullTime
	err := r.queryer.QueryRowContext(
		ctx,
		`UPDATE correlations
		 SET outcome = $1, feedback_received_at = now()
		 WHERE correlation_id = $2 AND session_id = $3
		   AND feedback_received_at IS NULL
		   AND EXISTS (SELECT 1 FROM sessions WHERE id = $3 AND namespace = $4)
		 RETURNING correlation_id, session_id, request_type, request_meta, outcome, feedback_received_at, created_at`,
		outcome, correlationID, sessionID, namespace,
	).Scan(&rec.CorrelationID, &rec.SessionID, &rec.RequestType, &rec.RequestMeta, &rec.Outcome, &feedbackAt, &rec.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return CorrelationRecord{}, ErrCorrelationNotFound
	}
	if err != nil {
		return CorrelationRecord{}, fmt.Errorf("%w: update outcome for correlation in session %d in %q: %w",
			ErrStorageBackend, sessionID, namespace, err)
	}
	rec.Namespace = namespace
	if feedbackAt.Valid {
		rec.FeedbackReceivedAt = feedbackAt.Time
	}
	return rec, nil
}
