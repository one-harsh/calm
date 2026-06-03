// Copyright 2026 The CALM Authors
// SPDX-License-Identifier: Apache-2.0

package db

import (
	"context"
	"fmt"
	"strings"

	logging "github.com/one-harsh/context-logging"
)

type chunksRepo struct {
	queryer queryer
	logger  *logging.Logger
}

func (r *chunksRepo) DeleteForSource(ctx context.Context, sourceID int64) error {
	if _, err := r.queryer.ExecContext(ctx, `DELETE FROM chunks WHERE source_id = $1`, sourceID); err != nil {
		return fmt.Errorf("%w: clear chunks for source %d: %w", ErrStorageBackend, sourceID, err)
	}
	return nil
}

func (r *chunksRepo) Insert(ctx context.Context, sourceID int64, chunks []Chunk) error {
	if len(chunks) == 0 {
		return nil
	}
	placeholders := make([]string, 0, len(chunks))
	args := make([]any, 0, len(chunks)*4)
	pos := 1
	for _, c := range chunks {
		placeholders = append(placeholders,
			fmt.Sprintf("($%d, $%d, $%d, $%d)", pos, pos+1, pos+2, pos+3))
		args = append(args, sourceID, c.Title, c.Content, c.ContentType)
		pos += 4
	}
	query := `INSERT INTO chunks (source_id, title, content, content_type) VALUES ` + strings.Join(placeholders, ", ") //nolint:gosec // operator-controlled column list, value placeholders
	if _, err := r.queryer.ExecContext(ctx, query, args...); err != nil {
		return fmt.Errorf("%w: insert %d chunks for source %d: %w",
			ErrStorageBackend, len(chunks), sourceID, err)
	}
	return nil
}
