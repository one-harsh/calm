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

	logging "github.com/one-harsh/context-logging"
)

const (
	defaultSearchLimit = 5
	snippetRadius      = 50
)

type sourcesRepo struct {
	queryer queryer
	logger  *logging.Logger
}

func (r *sourcesRepo) Upsert(ctx context.Context, namespace string, sessionID int64, source string) (int64, bool, error) {
	if namespace == "" {
		return 0, false, ErrNamespaceRequired
	}
	var id int64
	var created bool
	err := r.queryer.QueryRowContext(ctx,
		`INSERT INTO sources (session_id, label)
		 SELECT $2, $3 WHERE EXISTS (SELECT 1 FROM sessions WHERE id = $2 AND namespace = $1)
		 ON CONFLICT (session_id, label) DO UPDATE SET indexed_at = now()
		 RETURNING id, (xmax = 0)`,
		namespace, sessionID, source,
	).Scan(&id, &created)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, false, ErrSessionNotFound
	}
	if err != nil {
		return 0, false, fmt.Errorf("%w: upsert source %q for session %d in %q: %w",
			ErrStorageBackend, source, sessionID, namespace, err)
	}
	return id, created, nil
}

func (r *sourcesRepo) List(ctx context.Context, namespace string, sessionID int64) ([]SourceSummary, error) {
	if namespace == "" {
		return nil, ErrNamespaceRequired
	}

	const query = `SELECT s.label, s.indexed_at, COUNT(c.id)
	               FROM sources s
	               LEFT JOIN chunks c ON c.source_id = s.id
	               WHERE s.session_id = $1
	                 AND EXISTS (SELECT 1 FROM sessions WHERE id = $1 AND namespace = $2)
	               GROUP BY s.id, s.label, s.indexed_at
	               ORDER BY s.indexed_at DESC`

	rows, err := r.queryer.QueryContext(ctx, query, sessionID, namespace)
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

// HLD-DEVIATION: the HLD search section specifies a two-layer fallback
// (BM25 RRF → trigram) with title-weighted ranking; smoke does a single
// case-insensitive literal-substring scan over title+content, unranked
// (id order), and tags every hit match_layer="primary".
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

	limit := in.Limit
	if limit == 0 {
		limit = defaultSearchLimit
	}

	const query = `SELECT c.title, c.content, s.label
	               FROM chunks c
	               JOIN sources s ON c.source_id = s.id
	               WHERE s.session_id = $1
	                 AND EXISTS (SELECT 1 FROM sessions WHERE id = $1 AND namespace = $5)
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
	rows, err := r.queryer.QueryContext(ctx, query, sessionID, source, q, limit, namespace)
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
