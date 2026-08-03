// Copyright 2026 The CALM Authors
// SPDX-License-Identifier: Apache-2.0

package search

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"unicode/utf8"

	logging "github.com/one-harsh/context-logging"
	"github.com/stretchr/testify/mock"

	"github.com/one-harsh/calm/internal/db"
)

func docChunk(title, content, source string) db.DocChunk {
	return db.DocChunk{Title: title, Content: content, Source: source}
}

func docHitSize(t *testing.T, ch db.DocChunk) int {
	t.Helper()
	size, err := wireSize(db.SearchHit{
		Title: ch.Title, Snippet: ch.Content, Source: ch.Source, MatchLayer: matchLayerDocument,
	})
	if err != nil {
		t.Fatalf("wireSize: %v", err)
	}
	return size
}

func docService(t *testing.T, chunks []db.DocChunk, hasMore bool) *Service {
	t.Helper()
	sources := db.NewMockSourcesRepo(t)
	sources.EXPECT().ChunksInOrder(mock.Anything, "ns-a", mock.Anything).Return(chunks, hasMore, nil).Once()
	corr := db.NewMockCorrelationsRepo(t)
	corr.EXPECT().Insert(
		mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything,
	).Return(nil).Maybe()
	dal := db.NewMockDAL(t)
	dal.EXPECT().Sources().Return(sources).Maybe()
	dal.EXPECT().Correlations().Return(corr).Maybe()
	return New(dal, logging.Nop())
}

func TestDocumentOrder_AllChunksFitOnePage(t *testing.T) {
	chunks := []db.DocChunk{
		docChunk("a", "alpha content", "cap.log"),
		docChunk("b", "beta content", "cap.log"),
		docChunk("c", "gamma content", "cap.log"),
	}
	svc := docService(t, chunks, false)

	got, err := svc.DocumentOrder(context.Background(), "ns-a", 1, mustCorrelationID(t), "cap.log", 5, 0, bigBudget)
	if err != nil {
		t.Fatalf("DocumentOrder: %v", err)
	}
	if len(got.Queries) != 1 || got.Queries[0].Query != "" {
		t.Fatalf("envelope = %+v; want one query with empty string", got.Queries)
	}
	hits := got.Queries[0].Hits
	if len(hits) != 3 {
		t.Fatalf("hits = %d; want 3", len(hits))
	}
	for i, h := range hits {
		if h.MatchLayer != matchLayerDocument {
			t.Errorf("hit %d match_layer = %q; want document", i, h.MatchLayer)
		}
		if h.Snippet != chunks[i].Content {
			t.Errorf("hit %d snippet = %q; want full content %q", i, h.Snippet, chunks[i].Content)
		}
		if h.Truncated {
			t.Errorf("hit %d truncated; want whole chunk", i)
		}
	}
	if got.NextOffset != nil {
		t.Errorf("next_offset = %d; want absent on final page", *got.NextOffset)
	}
	if got.BudgetExceeded || got.Queries[0].BudgetOmitted != 0 {
		t.Errorf("exceeded=%v omitted=%d; want false/0", got.BudgetExceeded, got.Queries[0].BudgetOmitted)
	}
}

func TestDocumentOrder_LimitBindsBeforeBudget(t *testing.T) {
	chunks := []db.DocChunk{
		docChunk("a", "one", "cap.log"),
		docChunk("b", "two", "cap.log"),
		docChunk("c", "three", "cap.log"),
	}
	svc := docService(t, chunks, true)

	got, err := svc.DocumentOrder(context.Background(), "ns-a", 1, mustCorrelationID(t), "cap.log", 3, 10, bigBudget)
	if err != nil {
		t.Fatalf("DocumentOrder: %v", err)
	}
	if len(got.Queries[0].Hits) != 3 {
		t.Fatalf("hits = %d; want 3 (full page)", len(got.Queries[0].Hits))
	}
	if got.NextOffset == nil || *got.NextOffset != 13 {
		t.Errorf("next_offset = %v; want 13 (offset 10 + 3 consumed)", got.NextOffset)
	}
	if got.BudgetExceeded {
		t.Error("budget_exceeded = true; want false (limit, not budget, bound the page)")
	}
}

func TestDocumentOrder_BudgetBindsMidPage(t *testing.T) {
	chunks := []db.DocChunk{
		docChunk("a", "identical body", "cap.log"),
		docChunk("b", "identical body", "cap.log"),
		docChunk("c", "identical body", "cap.log"),
	}
	unit := docHitSize(t, chunks[0])
	budget := 2 * unit
	svc := docService(t, chunks, false)

	got, err := svc.DocumentOrder(context.Background(), "ns-a", 1, mustCorrelationID(t), "cap.log", 5, 0, budget)
	if err != nil {
		t.Fatalf("DocumentOrder: %v", err)
	}
	if len(got.Queries[0].Hits) != 2 {
		t.Fatalf("hits = %d; want 2 (budget admits two)", len(got.Queries[0].Hits))
	}
	if got.Queries[0].BudgetOmitted != 1 {
		t.Errorf("budget_omitted = %d; want 1 (one dropped chunk leads the next page)", got.Queries[0].BudgetOmitted)
	}
	if !got.BudgetExceeded {
		t.Error("budget_exceeded = false; want true")
	}
	if got.NextOffset == nil || *got.NextOffset != 2 {
		t.Errorf("next_offset = %v; want 2 (points at the dropped chunk)", got.NextOffset)
	}
	if got.ByteBudgetUsed != 2*unit {
		t.Errorf("byte_budget_used = %d; want %d (two whole chunks, no overshoot)", got.ByteBudgetUsed, 2*unit)
	}
}

func TestDocumentOrder_FirstOfPageTruncates(t *testing.T) {
	content := strings.Repeat("A", 600)
	ch := docChunk("big", content, "cap.log")
	whole := docHitSize(t, ch)
	budget := whole / 2
	svc := docService(t, []db.DocChunk{ch}, false)

	got, err := svc.DocumentOrder(context.Background(), "ns-a", 1, mustCorrelationID(t), "cap.log", 5, 0, budget)
	if err != nil {
		t.Fatalf("DocumentOrder: %v", err)
	}
	hits := got.Queries[0].Hits
	if len(hits) != 1 {
		t.Fatalf("hits = %d; want 1 (truncated head)", len(hits))
	}
	h := hits[0]
	if !h.Truncated {
		t.Error("truncated = false; want true for an over-budget head chunk")
	}
	if !strings.HasPrefix(content, h.Snippet) || len(h.Snippet) >= len(content) {
		t.Errorf("snippet is not a strict exact-text prefix of the chunk (content-fidelity)")
	}
	if got.ByteBudgetUsed > budget {
		t.Errorf("byte_budget_used %d overshot budget %d — strict contract violated", got.ByteBudgetUsed, budget)
	}
	if got.Queries[0].BudgetOmitted != 0 {
		t.Errorf("budget_omitted = %d; want 0 (a truncated chunk is delivered, not dropped)", got.Queries[0].BudgetOmitted)
	}
	if !got.BudgetExceeded {
		t.Error("budget_exceeded = false; want true")
	}
}

func TestDocumentOrder_TruncatedCountsAsConsumed(t *testing.T) {
	big := docChunk("big", strings.Repeat("B", 600), "cap.log")
	small := docChunk("small", "tail", "cap.log")
	budget := docHitSize(t, big) / 2
	svc := docService(t, []db.DocChunk{big, small}, false)

	got, err := svc.DocumentOrder(context.Background(), "ns-a", 1, mustCorrelationID(t), "cap.log", 5, 4, budget)
	if err != nil {
		t.Fatalf("DocumentOrder: %v", err)
	}
	if len(got.Queries[0].Hits) != 1 || !got.Queries[0].Hits[0].Truncated {
		t.Fatalf("hits = %+v; want a single truncated head", got.Queries[0].Hits)
	}
	if got.NextOffset == nil || *got.NextOffset != 5 {
		t.Errorf("next_offset = %v; want 5 (offset 4 + 1 consumed, past the truncated chunk)", got.NextOffset)
	}
}

func TestDocumentOrder_NextOffsetPresentAbsentMatrix(t *testing.T) {
	fit := func() []db.DocChunk {
		return []db.DocChunk{docChunk("a", "x", "s"), docChunk("b", "y", "s")}
	}

	t.Run("limit_full_probe", func(t *testing.T) {
		svc := docService(t, fit(), true)
		got, _ := svc.DocumentOrder(context.Background(), "ns-a", 1, mustCorrelationID(t), "s", 2, 0, bigBudget)
		if got.NextOffset == nil || *got.NextOffset != 2 {
			t.Errorf("next_offset = %v; want 2", got.NextOffset)
		}
	})

	t.Run("final_page", func(t *testing.T) {
		svc := docService(t, fit(), false)
		got, _ := svc.DocumentOrder(context.Background(), "ns-a", 1, mustCorrelationID(t), "s", 5, 0, bigBudget)
		if got.NextOffset != nil {
			t.Errorf("next_offset = %d; want absent", *got.NextOffset)
		}
	})

	t.Run("budget_drop", func(t *testing.T) {
		chunks := []db.DocChunk{docChunk("a", "body", "s"), docChunk("b", "body", "s")}
		budget := docHitSize(t, chunks[0])
		svc := docService(t, chunks, false)
		got, _ := svc.DocumentOrder(context.Background(), "ns-a", 1, mustCorrelationID(t), "s", 5, 0, budget)
		if got.NextOffset == nil || *got.NextOffset != 1 {
			t.Errorf("next_offset = %v; want 1 (drop at index 1)", got.NextOffset)
		}
	})

	t.Run("truncation_with_remainder", func(t *testing.T) {
		big := docChunk("a", strings.Repeat("Z", 600), "s")
		budget := docHitSize(t, big) / 2
		svc := docService(t, []db.DocChunk{big, docChunk("b", "tail", "s")}, false)
		got, _ := svc.DocumentOrder(context.Background(), "ns-a", 1, mustCorrelationID(t), "s", 5, 0, budget)
		if got.NextOffset == nil || *got.NextOffset != 1 {
			t.Errorf("next_offset = %v; want 1 (past the truncated head)", got.NextOffset)
		}
	})
}

func TestDocumentOrder_EmptySourceAndOffsetPastEnd(t *testing.T) {
	svc := docService(t, []db.DocChunk{}, false)
	got, err := svc.DocumentOrder(context.Background(), "ns-a", 1, mustCorrelationID(t), "missing", 5, 999, bigBudget)
	if err != nil {
		t.Fatalf("DocumentOrder: %v", err)
	}
	if len(got.Queries) != 1 || len(got.Queries[0].Hits) != 0 {
		t.Fatalf("hits = %+v; want an empty page envelope", got.Queries)
	}
	if got.NextOffset != nil {
		t.Errorf("next_offset = %d; want absent for an empty final page", *got.NextOffset)
	}
	if got.BudgetExceeded || got.Queries[0].BudgetOmitted != 0 {
		t.Errorf("exceeded=%v omitted=%d; want false/0 (nothing to serve)", got.BudgetExceeded, got.Queries[0].BudgetOmitted)
	}
}

func TestDocumentOrder_HeadChunkBelowScaffolding(t *testing.T) {
	ch := docChunk("big", strings.Repeat("Q", 400), "s")
	svc := docService(t, []db.DocChunk{ch}, false)

	got, err := svc.DocumentOrder(context.Background(), "ns-a", 1, mustCorrelationID(t), "s", 5, 7, 1)
	if err != nil {
		t.Fatalf("DocumentOrder: %v", err)
	}
	if len(got.Queries[0].Hits) != 0 {
		t.Fatalf("hits = %+v; want empty (nothing fits a 1-byte budget)", got.Queries[0].Hits)
	}
	if !got.BudgetExceeded {
		t.Error("budget_exceeded = false; want true")
	}
	if got.NextOffset == nil || *got.NextOffset != 7 {
		t.Errorf("next_offset = %v; want 7 (== offset: raise budget, retry in place)", got.NextOffset)
	}
	if got.ByteBudgetUsed != 0 {
		t.Errorf("byte_budget_used = %d; want 0 (strict, no overshoot)", got.ByteBudgetUsed)
	}
	if got.Queries[0].BudgetOmitted != 1 {
		t.Errorf("budget_omitted = %d; want 1 (the head chunk was budget-dropped whole)", got.Queries[0].BudgetOmitted)
	}
}

func TestTruncatedDocHit_RuneSafeAndFits(t *testing.T) {
	ch := docChunk("t", strings.Repeat("café ☃ ", 120), "s")

	prevBest := 0
	for _, budget := range []int{120, 240, 480, 960} {
		hit, size, ok := truncatedDocHit(ch, budget)
		if !ok {
			t.Fatalf("budget %d: ok=false; want a fitting prefix", budget)
		}
		if size > budget {
			t.Errorf("budget %d: size %d overshot", budget, size)
		}
		if !utf8.ValidString(hit.Snippet) {
			t.Errorf("budget %d: snippet split a UTF-8 rune", budget)
		}
		if !strings.HasPrefix(ch.Content, hit.Snippet) {
			t.Errorf("budget %d: snippet is not an exact-text prefix", budget)
		}
		if !hit.Truncated || hit.MatchLayer != matchLayerDocument {
			t.Errorf("budget %d: hit flags = %+v; want truncated document hit", budget, hit)
		}
		best := utf8.RuneCountInString(hit.Snippet)
		if best < prevBest {
			t.Errorf("budget %d: prefix runes %d shrank below %d — not monotone in budget", budget, best, prevBest)
		}
		prevBest = best
	}

	if _, _, ok := truncatedDocHit(ch, 1); ok {
		t.Error("ok=true at a 1-byte budget; want false (not even one rune fits)")
	}
}

func TestDocumentOrder_CorrelationMetaCarriesModeAndHitsDocument(t *testing.T) {
	chunks := []db.DocChunk{docChunk("a", "one", "s"), docChunk("b", "two", "s")}
	corrID := mustCorrelationID(t)

	sources := db.NewMockSourcesRepo(t)
	sources.EXPECT().ChunksInOrder(mock.Anything, "ns-a", mock.Anything).Return(chunks, false, nil).Once()
	corr := db.NewMockCorrelationsRepo(t)
	corr.EXPECT().Insert(
		mock.Anything, "ns-a", int64(1), corrID[:], "search",
		mock.MatchedBy(func(meta []byte) bool {
			var got map[string]any
			if err := json.Unmarshal(meta, &got); err != nil {
				return false
			}
			_, hasAllocator := got["allocator"]
			return got["mode"] == modeDocument &&
				got["hits_document"] == float64(2) &&
				got["hit_count"] == float64(2) &&
				got["hits_primary"] == float64(0) &&
				got["hits_trigram"] == float64(0) &&
				!hasAllocator
		}),
	).Return(nil).Once()
	dal := db.NewMockDAL(t)
	dal.EXPECT().Sources().Return(sources).Maybe()
	dal.EXPECT().Correlations().Return(corr).Maybe()
	svc := New(dal, logging.Nop())

	if _, err := svc.DocumentOrder(context.Background(), "ns-a", 1, corrID, "s", 5, 0, bigBudget); err != nil {
		t.Fatalf("DocumentOrder: %v", err)
	}
}

func TestDocumentOrder_CorrelationMetaCarriesIntentZeroMatchZero(t *testing.T) {
	chunks := []db.DocChunk{docChunk("a", "one", "s")}
	corrID := mustCorrelationID(t)

	sources := db.NewMockSourcesRepo(t)
	sources.EXPECT().ChunksInOrder(mock.Anything, "ns-a", mock.Anything).Return(chunks, false, nil).Once()
	corr := db.NewMockCorrelationsRepo(t)
	corr.EXPECT().Insert(
		mock.Anything, "ns-a", int64(1), corrID[:], "search",
		mock.MatchedBy(func(meta []byte) bool {
			var got map[string]any
			if err := json.Unmarshal(meta, &got); err != nil {
				return false
			}
			v, ok := got["intent_zero_match"]
			return ok && v == float64(0)
		}),
	).Return(nil).Once()
	dal := db.NewMockDAL(t)
	dal.EXPECT().Sources().Return(sources).Maybe()
	dal.EXPECT().Correlations().Return(corr).Maybe()
	svc := New(dal, logging.Nop())

	if _, err := svc.DocumentOrder(context.Background(), "ns-a", 1, corrID, "s", 5, 0, bigBudget); err != nil {
		t.Fatalf("DocumentOrder: %v", err)
	}
}
