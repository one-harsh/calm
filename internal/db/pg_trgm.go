// Copyright 2026 The CALM Authors
// SPDX-License-Identifier: Apache-2.0

package db

import (
	"context"
	"fmt"
	"strings"
)

const (
	// pg_trgm trigrams are 3-char windows over the padded input. Tokens
	// shorter than this contribute too few trigrams to filter usefully and
	// would over-include candidates if kept; drop them at normalization.
	minTrigramTermLen = 3
	// Per-term AND across the trigram filter means input length translates
	// linearly to SQL clauses + bind params. Search-query length has no API
	// ceiling, so the cap is the load-bearing availability check; 8 is large
	// enough for legitimate identifier-heavy queries and small enough to
	// keep the SQL bounded under adversarial / LLM-verbose input.
	maxTrigramTerms = 8
)

// trigramStopwords are English function words that don't carry trigram signal
// but would, via the per-term `<<%` AND filter, exclude every chunk that
// doesn't word-similar-match them — without contributing recall. The list is
// deliberately small (no full-linguistic stopword set) so that distinctive
// content words ("show", "find") don't get stripped from legitimate queries.
var trigramStopwords = map[string]bool{
	"the": true, "an": true, "of": true, "and": true, "or": true,
	"in": true, "on": true, "at": true, "to": true, "for": true, "with": true,
	"is": true, "are": true, "was": true, "were": true, "be": true, "been": true,
	"do": true, "did": true, "does": true, "done": true,
	"by": true, "as": true, "from": true, "into": true, "than": true,
	"what": true, "where": true, "when": true, "why": true, "how": true,
	"who": true, "which": true, "that": true, "this": true, "it": true, "its": true,
}

// normalizeTrigramTerms returns the subset of whitespace-tokenized q that
// carries trigram signal: at least minTrigramTermLen chars, not a
// trigramStopwords entry, deduplicated case-insensitively, and capped to
// maxTrigramTerms in input order. Order is preserved so workload-supplied
// term emphasis isn't reshuffled; dedup uses lowercase keys but original
// case flows to the SQL (pg_trgm normalizes case internally, so this is for
// readability, not correctness).
func normalizeTrigramTerms(q string) []string {
	seen := make(map[string]bool)
	out := make([]string, 0, maxTrigramTerms)
	for _, raw := range strings.Fields(q) {
		if len(raw) < minTrigramTermLen {
			continue
		}
		lower := strings.ToLower(raw)
		if trigramStopwords[lower] {
			continue
		}
		if seen[lower] {
			continue
		}
		seen[lower] = true
		out = append(out, raw)
		if len(out) == maxTrigramTerms {
			return out
		}
	}
	return out
}

// Layer-2 partial-term fallback: pg_trgm's gin_trgm_ops indexes serve the
// strict_word_similarity operator (`<<%`), which scores how closely the query
// matches a whole-word-bounded substring of the target. The per-term clauses
// probe the index; strict_word_similarity() in the ORDER BY ranks the
// (already filtered) candidate set. Per-term AND across query tokens — each
// clause disjuncts title and content. Ordering mirrors layer 1's 2:1 title
// weighting via the same strict_word_similarity, computed against the whole
// query string.
//
// `<<%` (strict) over `<%` (looser word) is deliberate: `<<%` requires the
// query to align with a word boundary in the target, which prevents
// cross-term false positives in multi-term AND filters (a chunk containing
// only "sessionPoolWrapper" must NOT pass the "userPoolWrapper" clause).
// Threshold: pg_trgm's `strict_word_similarity_threshold` (default 0.5).
//
// Layer-1 chunk ids are excluded via a bigint-array parameter — an empty
// array literal '{}' is universally true, so a layer-1 zero-hit query passes
// through with no dedup work.

func trigramCandidatesSQL(termCount int) string {
	var clauses strings.Builder
	for i := 0; i < termCount; i++ {
		placeholder := i + 7
		fmt.Fprintf(&clauses,
			"\n\t\t  AND ($%d <<%% c.title OR $%d <<%% c.content)",
			placeholder, placeholder)
	}
	return fmt.Sprintf(`
		SELECT c.id, c.title, c.content, s.label
		FROM chunks c
		JOIN sources s ON c.source_id = s.id
		WHERE s.session_id = $1
		  AND EXISTS (SELECT 1 FROM sessions WHERE id = $1 AND namespace = $3)
		  AND ($2 = '' OR s.label = $2)
		  AND c.id <> ALL($6::bigint[])%s
		ORDER BY (2.0 * strict_word_similarity($4, c.title)
		        + 1.0 * strict_word_similarity($4, c.content)) DESC,
		         c.id ASC
		LIMIT $5`, clauses.String())
}

func (r *sourcesRepo) trigramCandidates(
	ctx context.Context,
	namespace string,
	sessionID int64,
	source string,
	q string,
	limit int,
	excludeIDs []int64,
) ([]SearchHit, error) {
	if limit <= 0 {
		return []SearchHit{}, nil
	}
	terms := normalizeTrigramTerms(q)
	if len(terms) == 0 {
		return []SearchHit{}, nil
	}

	args := []any{sessionID, source, namespace, q, limit, excludeIDs}
	for _, term := range terms {
		args = append(args, term)
	}

	rows, err := r.queryer.QueryContext(ctx, trigramCandidatesSQL(len(terms)), args...)
	if err != nil {
		return nil, fmt.Errorf("%w: trigram search session %d in %q: %w",
			ErrStorageBackend, sessionID, namespace, err)
	}
	defer func() { _ = rows.Close() }()

	hits := []SearchHit{}
	for rows.Next() {
		var (
			id      int64
			title   string
			content string
			label   string
		)
		if err := rows.Scan(&id, &title, &content, &label); err != nil {
			return nil, fmt.Errorf("%w: scan trigram hit for session %d in %q: %w",
				ErrStorageBackend, sessionID, namespace, err)
		}
		hits = append(hits, SearchHit{
			Title:      title,
			Snippet:    snippet(content, q),
			Source:     label,
			MatchLayer: "trigram",
		})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("%w: iterate trigram hits for session %d in %q: %w",
			ErrStorageBackend, sessionID, namespace, err)
	}
	return hits, nil
}
