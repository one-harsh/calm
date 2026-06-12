// Copyright 2026 The CALM Authors
// SPDX-License-Identifier: Apache-2.0

package db

import (
	"context"
	"fmt"

	logging "github.com/one-harsh/context-logging"
)

// Vocabulary writes are single-statement leaves: term derivation happens inside
// each statement, not as Go-side read-then-write steps in the service's WithTx —
// splitting would ship the session's vocabulary across the wire twice per
// re-index and add parameter-limit failure modes without making the service
// choreography more legible. Tokenization uses to_tsvector with the same
// text-search configs the bm25 indexes name, so "counted" and "queryable" align
// by construction; to_tsvector dedupes lexemes per document, making
// COUNT(*) GROUP BY word exactly doc_freq's unit (chunks containing the word).
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
