// Copyright 2026 The CALM Authors
// SPDX-License-Identifier: Apache-2.0

package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/jackc/pgx/v5/pgconn"
	logging "github.com/one-harsh/context-logging"
)

type sourcesRepo struct {
	queryer queryer
	logger  *logging.Logger
}

func (r *sourcesRepo) Index(ctx context.Context, namespace string, in IndexInput) (bool, error) {
	if namespace == "" {
		return false, ErrNamespaceRequired
	}
	if in.Source == "" {
		return false, ErrSourceRequired
	}
	created := false
	err := inTx(ctx, r.queryer, func(tx *sql.Tx) error {
		if err := verifySessionInNamespace(ctx, tx, namespace, in.SessionID); err != nil {
			return err
		}
		// idempotent-indexing invariant: re-ingesting a source replaces its prior content.
		// xmax = 0 distinguishes a fresh insert from an on-conflict update.
		var sourceID int64
		err := tx.QueryRowContext(ctx,
			`INSERT INTO sources (session_id, label) VALUES ($1, $2)
			 ON CONFLICT (session_id, label) DO UPDATE SET indexed_at = now()
			 RETURNING id, (xmax = 0)`,
			in.SessionID, in.Source,
		).Scan(&sourceID, &created)
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
	return created, err
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

const (
	defaultSearchLimit = 5
	snippetRadius      = 50
)

// HLD-DEVIATION: the HLD search section specifies a three-layer fallback
// (BM25 RRF → trigram → fuzzy) with title-weighted ranking; smoke does a
// single case-insensitive literal-substring scan over title+content,
// unranked (id order), and tags every hit match_layer="primary".
func (r *sourcesRepo) Search(ctx context.Context, namespace string, in SearchInput) ([]SearchResult, error) {
	if namespace == "" {
		return nil, ErrNamespaceRequired
	}
	if len(in.Queries) == 0 {
		return nil, ErrQueryRequired
	}
	// An empty query string is a literal-substring match against everything
	// (position('' in x) > 0 is always true), so reject it here rather than
	// rely on the HTTP layer's minLength validation.
	for _, q := range in.Queries {
		if q == "" {
			return nil, ErrQueryRequired
		}
	}
	if in.Limit < 0 {
		return nil, ErrInvalidLimit
	}
	if err := verifySessionInNamespace(ctx, r.queryer, namespace, in.SessionID); err != nil {
		return nil, err
	}

	limit := in.Limit
	if limit == 0 {
		limit = defaultSearchLimit
	}

	const query = `SELECT c.title, c.content, s.label
	               FROM chunks c JOIN sources s ON c.source_id = s.id
	               WHERE s.session_id = $1
	                 AND ($2 = '' OR s.label = $2)
	                 AND (position(lower($3) in lower(c.content)) > 0
	                      OR position(lower($3) in lower(c.title)) > 0)
	               ORDER BY c.id
	               LIMIT $4`

	// PERF: queries run sequentially — intentional for the smoke substring scan,
	// where the win isn't there. Once BM25 search lands, fan these out with an
	// errgroup (~10× wall-clock); don't parallelize the substring path prematurely.
	out := make([]SearchResult, 0, len(in.Queries))
	for _, q := range in.Queries {
		hits, err := r.searchOne(ctx, namespace, query, in.SessionID, in.Source, q, limit)
		if err != nil {
			return nil, err
		}
		out = append(out, SearchResult{Query: q, Hits: hits})
	}
	return out, nil
}

func (r *sourcesRepo) searchOne(ctx context.Context, namespace, query string, sessionID int64, source, q string, limit int) ([]SearchHit, error) {
	rows, err := r.queryer.QueryContext(ctx, query, sessionID, source, q, limit)
	if err != nil {
		return nil, fmt.Errorf("%w: search session %d in %q: %w", ErrStorageBackend, sessionID, namespace, err)
	}
	defer func() { _ = rows.Close() }()

	hits := []SearchHit{}
	for rows.Next() {
		var title, content, label string
		if err := rows.Scan(&title, &content, &label); err != nil {
			return nil, fmt.Errorf("%w: scan search hit for session %d in %q: %w", ErrStorageBackend, sessionID, namespace, err)
		}
		hits = append(hits, SearchHit{
			Title:      title,
			Snippet:    snippet(content, q),
			Source:     label,
			MatchLayer: "primary",
		})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("%w: iterate search hits for session %d in %q: %w", ErrStorageBackend, sessionID, namespace, err)
	}
	return hits, nil
}

// snippet returns a ±snippetRadius-rune window of content around the first
// case-insensitive match of query — exact text (content-fidelity), never
// paraphrased. If the match was on the title (not present in content), it
// returns a leading window of the content instead.
func snippet(content, query string) string {
	runes := []rune(content)
	at := strings.Index(strings.ToLower(content), strings.ToLower(query))
	if at < 0 {
		if len(runes) <= 2*snippetRadius {
			return content
		}
		return string(runes[:2*snippetRadius])
	}
	start := utf8.RuneCountInString(content[:at])
	end := start + utf8.RuneCountInString(query)
	from := max(start-snippetRadius, 0)
	to := min(end+snippetRadius, len(runes))
	return string(runes[from:to])
}
