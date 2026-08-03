// Copyright 2026 The CALM Authors
// SPDX-License-Identifier: Apache-2.0

package search

import (
	"context"
	"encoding/json"

	"github.com/google/uuid"
	logging "github.com/one-harsh/context-logging"

	"github.com/one-harsh/calm/internal/db"
	"github.com/one-harsh/calm/internal/obs"
)

// DocumentOrder's assembly is deterministic and it is forbidden to
// skip ahead, so at most one chunk is budget-dropped per page
// (budget_omitted ∈ {0,1} by construction).
func (s *Service) DocumentOrder(
	ctx context.Context,
	namespace string,
	sessionID int64,
	correlationID uuid.UUID,
	source string,
	limit, offset, budgetBytes int,
) (Result, error) {
	chunks, hasMoreBeyondLimit, err := s.store.Sources().ChunksInOrder(ctx, namespace, db.DocOrderInput{
		SessionID: sessionID,
		Source:    source,
		Limit:     limit,
		Offset:    offset,
	})
	if err != nil {
		return Result{}, err
	}

	hits := []db.SearchHit{}
	used := 0
	consumed := 0
	budgetOmitted := 0
	budgetExceeded := false
	headTruncated := false
	headNoFit := false

	for _, ch := range chunks {
		hit := db.SearchHit{
			Title:      ch.Title,
			Snippet:    ch.Content,
			Source:     ch.Source,
			MatchLayer: matchLayerDocument,
		}
		size, err := wireSize(hit)
		if err != nil {
			return Result{}, err
		}
		if used+size <= budgetBytes {
			hits = append(hits, hit)
			used += size
			consumed++
			continue
		}
		if len(hits) == 0 {
			// content-fidelity: truncate only to an exact rune prefix.
			th, thSize, ok := truncatedDocHit(ch, budgetBytes)
			if ok {
				hits = append(hits, th)
				used += thSize
				consumed++
				headTruncated = true
			} else {
				headNoFit = true
				budgetOmitted = 1
			}
			budgetExceeded = true
			break
		}
		// Document order forbids skipping a non-fitter; it leads the next page.
		budgetOmitted = 1
		budgetExceeded = true
		break
	}

	var hasMore bool
	switch {
	case headNoFit:
		// Preserve the cursor when no rune fits so a larger budget loses no content.
		hasMore = true
	case headTruncated:
		// A truncated chunk's tail is intentionally not a second document-order page.
		hasMore = len(chunks) > 1 || hasMoreBeyondLimit
	case budgetOmitted == 1:
		hasMore = true
	default:
		hasMore = hasMoreBeyondLimit
	}

	var nextOffset *int
	if hasMore {
		n := offset + consumed
		nextOffset = &n
	}

	result := Result{
		Queries: []QueryResult{{
			Query:         "",
			Hits:          hits,
			BudgetOmitted: budgetOmitted,
		}},
		ByteBudgetUsed: used,
		BudgetExceeded: budgetExceeded,
		BudgetBytes:    budgetBytes,
		NextOffset:     nextOffset,
	}

	if s.logger.Enabled(logging.DebugLevel) {
		s.logger.WithContext(ctx).Debug(
			"search executed",
			obs.ModeDocument,
			obs.SearchQueries(0),
			obs.SearchHitsTotal(consumed),
			obs.SearchHitsDocument(consumed),
			obs.SearchByteBudgetUsed(used),
			obs.SearchBudgetExhausted(budgetExceeded),
			obs.SearchResultsOmitted(budgetOmitted),
		)
	}
	s.captureDocumentCorrelation(ctx, namespace, sessionID, correlationID, consumed, budgetExceeded, used, budgetOmitted)
	return result, nil
}

// Serialized size is monotone by rune count, so binary search finds the longest
// content-fidelity prefix without splitting UTF-8.
func truncatedDocHit(ch db.DocChunk, budget int) (db.SearchHit, int, bool) {
	runes := []rune(ch.Content)
	build := func(n int) db.SearchHit {
		return db.SearchHit{
			Title:      ch.Title,
			Snippet:    string(runes[:n]),
			Source:     ch.Source,
			MatchLayer: matchLayerDocument,
			Truncated:  true,
		}
	}

	lo, hi, best := 1, len(runes), 0
	for lo <= hi {
		mid := (lo + hi) / 2
		size, err := wireSize(build(mid))
		if err != nil {
			return db.SearchHit{}, 0, false
		}
		if size <= budget {
			best = mid
			lo = mid + 1
		} else {
			hi = mid - 1
		}
	}
	if best < 1 {
		return db.SearchHit{}, 0, false
	}
	hit := build(best)
	size, err := wireSize(hit)
	if err != nil {
		return db.SearchHit{}, 0, false
	}
	return hit, size, true
}

func (s *Service) captureDocumentCorrelation(
	ctx context.Context,
	namespace string,
	sessionID int64,
	correlationID uuid.UUID,
	hitCount int,
	budgetExceeded bool,
	byteBudgetUsed, resultsOmitted int,
) {
	meta, err := json.Marshal(map[string]any{
		"mode":              modeDocument,
		"hits_primary":      0,
		"hits_trigram":      0,
		"hits_document":     hitCount,
		"hit_count":         hitCount,
		"snippet_fallbacks": 0,
		"budget_exceeded":   budgetExceeded,
		"byte_budget_used":  byteBudgetUsed,
		"results_omitted":   resultsOmitted,
		"intent_zero_match": 0,
	})
	if err != nil {
		s.logger.WithContext(ctx).Warn("correlation marshal failed",
			obs.RequestType(requestTypeSearch), logging.ErrorField(err))
		return
	}
	if err := s.store.Correlations().Insert(ctx, namespace, sessionID, correlationID[:], requestTypeSearch, meta); err != nil {
		s.logger.WithContext(ctx).Warn("correlation insert failed",
			obs.RequestType(requestTypeSearch), logging.ErrorField(err))
	}
}
