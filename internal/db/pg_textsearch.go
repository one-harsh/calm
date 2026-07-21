// Copyright 2026 The CALM Authors
// SPDX-License-Identifier: Apache-2.0

package db

import "fmt"

// Layer-1 search keeps ranking and lexical identity as separate concerns.
//
// pg_textsearch supplies the BM25 ranker: to_bm25query(...) builds the
// extension-specific query object, and column <@> bm25query scores a table
// column against the named BM25 index. Native Postgres text search supplies the
// lexical view: to_tsvector(...) tokenizes text into normalized lexemes under a
// text-search config ("english" stems prose; "simple" preserves code-shaped
// identifiers more directly). CALM uses the tsvector side for boolean gates and
// tiers, and the pg_textsearch side for final score ordering.
//
// The same tokenizer class is applied to both the query and the candidate
// document. That keeps the HLD knowledge-store contract concrete: every query
// term present ranks above partial matches, partial lexical overlap is still a
// primary-layer fallback, and BM25 is not allowed to backfill unrelated rows.

// bm25Class describes one tokenizer class of the layer-1 search surface: the
// text-search config the class's bm25 indexes were built with, the per-field
// index names, and the content_type predicate that partitions chunks between
// the classes.
type bm25Class struct {
	textConfig    string
	titleIndex    string
	contentIndex  string
	typePredicate string
}

var bm25Classes = [...]bm25Class{
	{
		textConfig:    "english",
		titleIndex:    "chunks_bm25_prose_title_idx",
		contentIndex:  "chunks_bm25_prose_content_idx",
		typePredicate: "c.content_type <> 'code'",
	},
	{
		textConfig:    "simple",
		titleIndex:    "chunks_bm25_code_title_idx",
		contentIndex:  "chunks_bm25_code_content_idx",
		typePredicate: "c.content_type = 'code'",
	},
}

// bm25Extension isolates extension-specific ranking SQL behind one seam:
// supporting another BM25-capable extension (pg_search) is a sibling
// implementation file plus a constructor change, not a refactor of the
// search path.
type bm25Extension interface {
	classCandidatesSQL(class bm25Class) string
}

// pgTextsearchExtension generates ranking SQL for pg_textsearch, whose query
// surface shapes the statement:
//   - `column <@> bm25query` returns the NEGATED BM25 score (ASC = most
//     relevant), and its left operand must be a table column — constants and
//     expressions error.
//   - to_bm25query takes the explicit index name; that is what selects one of
//     the four partial indexes (tokenizer class × field) and its corpus
//     statistics, which the score uses even under a sequential scan.
type pgTextsearchExtension struct{}

func (pgTextsearchExtension) classCandidatesSQL(class bm25Class) string {
	// Title weighted 2.0 vs content 1.0 (HLD's storage section); on negated
	// scores the weighting deepens title matches, keeping ASC = best.
	// matches_all (every query term present across title+content) tiers
	// AND-matches above OR-matches; matches_any gates out rows sharing no
	// lexeme with the query, which BM25 would otherwise score and backfill.
	// MATERIALIZED and OFFSET 0 are optimization fences, not noise: without
	// them the planner inlines the one-row CTE and pulls up the LATERAL,
	// re-evaluating the STABLE query-parsing calls per candidate row and the
	// document tsvector twice per row (once per matches_* column).
	return fmt.Sprintf(`
		WITH query AS MATERIALIZED (
			SELECT to_bm25query($4, '%[2]s')                   AS title_query,
			       to_bm25query($4, '%[3]s')                   AS content_query,
			       plainto_tsquery('%[1]s', $4)                AS and_query,
			       tsvector_to_array(to_tsvector('%[1]s', $4)) AS query_lexemes
		),
		candidates AS (
			SELECT c.id, c.title, c.content, c.content_type, s.label,
			       2.0 * (c.title   <@> q.title_query)
			     + 1.0 * (c.content <@> q.content_query)         AS score,
			       doc.vec @@ q.and_query                        AS matches_all,
			       tsvector_to_array(doc.vec) && q.query_lexemes AS matches_any
			FROM chunks c
			JOIN sources s ON c.source_id = s.id
			CROSS JOIN query q
			CROSS JOIN LATERAL (
				SELECT to_tsvector('%[1]s', c.title || ' ' || c.content) AS vec
				OFFSET 0
			) doc
			WHERE s.session_id = $1
			  AND EXISTS (SELECT 1 FROM sessions WHERE id = $1 AND namespace = $3)
			  AND ($2 = '' OR s.label = $2)
			  AND %[4]s
		)
		SELECT id, title, content, content_type, label, matches_all FROM candidates
		WHERE matches_any
		ORDER BY matches_all DESC, score ASC, id ASC
		LIMIT $5`,
		class.textConfig, class.titleIndex, class.contentIndex, class.typePredicate)
}
