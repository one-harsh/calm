// Copyright 2026 The CALM Authors
// SPDX-License-Identifier: Apache-2.0

package db

import (
	"cmp"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"slices"

	logging "github.com/one-harsh/context-logging"
)

const (
	defaultSearchLimit = 5
	rrfK               = 60
)

type sourcesRepo struct {
	queryer queryer
	logger  *logging.Logger
	bm25    bm25Extension
}

func (r *sourcesRepo) Upsert(ctx context.Context, namespace string, sessionID int64, source string) (int64, bool, error) {
	if namespace == "" {
		return 0, false, ErrNamespaceRequired
	}
	var id int64
	var created bool
	err := r.queryer.QueryRowContext(
		ctx,
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

// Single-query by design: the repo is tx-agnostic, so batch fan-out lives in
// the service layer that knows it holds a concurrency-safe *sql.DB.
func (r *sourcesRepo) Search(ctx context.Context, namespace string, in SearchInput) ([]SearchHit, error) {
	if namespace == "" {
		return nil, ErrNamespaceRequired
	}
	if in.Query == "" {
		return nil, ErrQueryRequired
	}
	if in.Limit < 0 {
		return nil, ErrInvalidLimit
	}

	limit := in.Limit
	if limit == 0 {
		limit = defaultSearchLimit
	}

	return r.searchOne(ctx, namespace, in.SessionID, in.Source, in.Query, limit)
}

type bm25Candidate struct {
	id          int64
	title       string
	content     string
	contentType string
	label       string
	matchesAll  bool
	rrf         float64
	// NEGATED BM25 (smaller = better); negated again into Relevance at hit
	// assembly. Feeds budget allocation, never the fused sort.
	score float64
}

// searchOne is the layer-1 query: each tokenizer class contributes its top
// 2×limit BM25-ranked candidates, fused via Reciprocal Rank Fusion (k=60) per
// HLD's layer-1-fusion contract. content_type partitions chunks between the
// classes, so the lists are disjoint and fusion reduces to interleaving —
// no shared-document reinforcement can occur. AND-matches outrank any RRF
// score (AND across query terms first, then the OR fallback).
func (r *sourcesRepo) searchOne(ctx context.Context, namespace string, sessionID int64, source, q string, limit int) ([]SearchHit, error) {
	fused := []bm25Candidate{}
	for _, class := range bm25Classes {
		cands, err := r.classCandidates(ctx, namespace, class, sessionID, source, q, 2*limit)
		if err != nil {
			return nil, err
		}
		fused = append(fused, cands...)
	}

	slices.SortFunc(fused, func(a, b bm25Candidate) int {
		if a.matchesAll != b.matchesAll {
			if a.matchesAll {
				return -1
			}
			return 1
		}
		if a.rrf != b.rrf {
			return cmp.Compare(b.rrf, a.rrf)
		}
		return cmp.Compare(a.id, b.id)
	})
	if len(fused) > limit {
		fused = fused[:limit]
	}

	hits := make([]SearchHit, 0, limit)
	for _, c := range fused {
		snip := extractSnippet(c.content, c.contentType, q)
		hits = append(hits, SearchHit{
			Title:           c.title,
			Snippet:         snip.text,
			SnippetFallback: snip.fallback,
			Source:          c.label,
			MatchLayer:      "primary",
			Relevance:       -c.score,
			MatchesAll:      c.matchesAll,
		})
	}

	if len(hits) < limit {
		excludeIDs := make([]int64, len(fused))
		for i, c := range fused {
			excludeIDs[i] = c.id
		}
		trigramHits, err := r.trigramCandidates(ctx, namespace, sessionID, source, q, limit-len(hits), excludeIDs)
		if err != nil {
			return nil, err
		}
		hits = append(hits, trigramHits...)
	}
	return hits, nil
}

func (r *sourcesRepo) classCandidates(ctx context.Context, namespace string, class bm25Class, sessionID int64, source, q string, k int) ([]bm25Candidate, error) {
	rows, err := r.queryer.QueryContext(ctx, r.bm25.classCandidatesSQL(class), sessionID, source, namespace, q, k)
	if err != nil {
		return nil, fmt.Errorf("%w: search session %d in %q: %w", ErrStorageBackend, sessionID, namespace, err)
	}
	defer func() { _ = rows.Close() }()

	cands := []bm25Candidate{}
	rank := 0
	for rows.Next() {
		var c bm25Candidate
		if err := rows.Scan(&c.id, &c.title, &c.content, &c.contentType, &c.label, &c.matchesAll, &c.score); err != nil {
			return nil, fmt.Errorf("%w: scan search hit for session %d in %q: %w", ErrStorageBackend, sessionID, namespace, err)
		}
		rank++
		c.rrf = 1.0 / float64(rrfK+rank)
		cands = append(cands, c)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("%w: iterate search hits for session %d in %q: %w", ErrStorageBackend, sessionID, namespace, err)
	}
	return cands, nil
}
