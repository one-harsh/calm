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
		// Chunk does not fit whole.
		if len(hits) == 0 {
			// Head chunk over budget: return an exact-text prefix that fills the
			// budget (content-fidelity — exact prefix, no paraphrase). If not
			// even one rune fits, the page is empty and the caller must raise
			// budget_bytes.
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
		// Mid-page non-fitter ends the page; document order forbids skipping it,
		// so it leads the next page (budget_omitted stays 1 by construction).
		budgetOmitted = 1
		budgetExceeded = true
		break
	}

	var hasMore bool
	switch {
	case headNoFit:
		// consumed==0, next_offset==offset: caller raises budget and retries at
		// the same cursor — no loss, no loop.
		hasMore = true
	case headTruncated:
		// The truncated chunk's own tail past the budget is unreadable in this
		// mode; more remains only if the page held further chunks or the probe
		// saw rows beyond the limit.
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

// truncatedDocHit returns a document-order hit whose snippet is the longest
// exact-text rune-prefix of the chunk whose truncated-flagged wire size fits
// budget. Binary search is valid because serialized size is monotone
// non-decreasing in prefix length — appending a rune never shortens the JSON.
// It never splits a UTF-8 rune (the prefix is taken on rune boundaries); ok is
// false when not even one rune fits.
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
