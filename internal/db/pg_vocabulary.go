// Copyright 2026 The CALM Authors
// SPDX-License-Identifier: Apache-2.0

package db

import (
	"context"
	"fmt"

	logging "github.com/one-harsh/context-logging"
)

// Vocabulary is the compact handle CALM returns for follow-up search. It uses
// the same tokenizer classes as layer-1 search, but counts chunk content rather
// than title text: titles are already returned in the ingest summary, while
// distinctive_terms describe the body vocabulary used for follow-up search.
//
// to_tsvector(...) is the boundary between raw text and vocabulary terms. It
// tokenizes each chunk under the chunk's text-search config ("english" for
// prose, "simple" for code), normalizes surface forms into lexemes, and records
// one searchable lexical view of the content. tsvector_to_array(...) emits the
// distinct lexemes from that vector, so COUNT(*) GROUP BY word is exactly
// doc_freq's unit: chunks containing the term.
//
// Vocabulary writes stay as single-statement leaves: deriving terms in SQL
// avoids Go-side read-then-write steps, large parameter lists, and shipping the
// session vocabulary across the wire during every re-index.
// Like the chunk leaf-writes, methods key off a source_id minted by the
// namespace-verified Sources.Upsert in the same transaction (a capability), and
// derive session_id from sources inside the statement.
type vocabularyRepo struct {
	queryer queryer
	logger  *logging.Logger
}

const vocabTermCounts = `
	SELECT w.word, COUNT(*) AS cnt
	FROM chunks c
	CROSS JOIN LATERAL unnest(tsvector_to_array(to_tsvector(
		CASE WHEN c.content_type = 'code' THEN 'simple'::regconfig ELSE 'english'::regconfig END,
		c.content))) AS w(word)
	WHERE c.source_id = $1
	GROUP BY w.word`

func (r *vocabularyRepo) DecrementForSource(ctx context.Context, sourceID int64) error {
	query := `
		WITH old AS (` + vocabTermCounts + `
		)
		UPDATE vocabulary v SET doc_freq = v.doc_freq - old.cnt
		FROM old, sources src
		WHERE src.id = $1 AND v.session_id = src.session_id AND v.word = old.word`
	if _, err := r.queryer.ExecContext(ctx, query, sourceID); err != nil {
		return fmt.Errorf("%w: decrement vocabulary for source %d: %w", ErrStorageBackend, sourceID, err)
	}
	return nil
}

func (r *vocabularyRepo) IncrementForSource(ctx context.Context, sourceID int64) error {
	// The GROUP BY in the derivation is load-bearing: without it, two chunks
	// sharing a word make ON CONFLICT DO UPDATE touch the same row twice →
	// "cannot affect row a second time".
	query := `
		INSERT INTO vocabulary (session_id, word, doc_freq)
		SELECT src.session_id, t.word, t.cnt
		FROM sources src
		CROSS JOIN (` + vocabTermCounts + `
		) t
		WHERE src.id = $1
		ON CONFLICT (session_id, word) DO UPDATE SET doc_freq = vocabulary.doc_freq + EXCLUDED.doc_freq`
	if _, err := r.queryer.ExecContext(ctx, query, sourceID); err != nil {
		return fmt.Errorf("%w: increment vocabulary for source %d: %w", ErrStorageBackend, sourceID, err)
	}
	return nil
}

// PruneZeros is a separate statement (not folded into the decrement via
// data-modifying CTEs): UPDATE+DELETE of the same rows in one statement has
// unsupported visibility semantics in Postgres. Running it last also lets a
// word decremented to 0 and re-added by the increment go 0→n without
// delete+reinsert churn.
func (r *vocabularyRepo) PruneZeros(ctx context.Context, sessionID int64) error {
	if _, err := r.queryer.ExecContext(ctx,
		`DELETE FROM vocabulary WHERE session_id = $1 AND doc_freq <= 0`, sessionID); err != nil {
		return fmt.Errorf("%w: prune vocabulary for session %d: %w", ErrStorageBackend, sessionID, err)
	}
	return nil
}

// TopByIDF orders by doc_freq ASC, not log(N/doc_freq) DESC: N (the session's
// chunk count) is fixed within one selection and log is monotone, so both
// orderings pick the identical set. word ASC pins ties deterministically.
func (r *vocabularyRepo) TopByIDF(ctx context.Context, sessionID int64, limit int) ([]string, error) {
	rows, err := r.queryer.QueryContext(ctx,
		`SELECT word FROM vocabulary WHERE session_id = $1 ORDER BY doc_freq ASC, word ASC LIMIT $2`,
		sessionID, limit)
	if err != nil {
		return nil, fmt.Errorf("%w: top vocabulary terms for session %d: %w", ErrStorageBackend, sessionID, err)
	}
	defer func() { _ = rows.Close() }()

	words := []string{}
	for rows.Next() {
		var w string
		if err := rows.Scan(&w); err != nil {
			return nil, fmt.Errorf("%w: scan vocabulary term for session %d: %w", ErrStorageBackend, sessionID, err)
		}
		words = append(words, w)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("%w: iterate vocabulary terms for session %d: %w", ErrStorageBackend, sessionID, err)
	}
	return words, nil
}
