// Copyright 2026 The CALM Authors
// SPDX-License-Identifier: Apache-2.0

package search

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/google/uuid"
	logging "github.com/one-harsh/context-logging"
	"github.com/stretchr/testify/mock"

	"github.com/one-harsh/calm/internal/db"
)

const bigBudget = 1 << 20

func mustCorrelationID(t *testing.T) uuid.UUID {
	t.Helper()
	id, err := uuid.NewV7()
	if err != nil {
		t.Fatalf("uuid.NewV7: %v", err)
	}
	return id
}

func serviceReturningHits(t *testing.T, hits []db.SearchHit, err error, calls int) (*Service, *db.MockCorrelationsRepo) {
	t.Helper()
	sources := db.NewMockSourcesRepo(t)
	sources.EXPECT().Search(mock.Anything, "ns-a", mock.Anything).Return(hits, err).Times(calls)
	corr := db.NewMockCorrelationsRepo(t)
	corr.EXPECT().Insert(
		mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything,
	).Return(nil).Maybe()
	dal := db.NewMockDAL(t)
	dal.EXPECT().Sources().Return(sources).Maybe()
	dal.EXPECT().Correlations().Return(corr).Maybe()
	return New(dal, logging.Nop()), corr
}

func TestSearch_HappyPathReturnsResults(t *testing.T) {
	hits := []db.SearchHit{
		{Title: "t1", Snippet: "s1", Source: "out", MatchLayer: "primary"},
		{Title: "t2", Snippet: "s2", Source: "out", MatchLayer: "primary"},
	}
	svc, _ := serviceReturningHits(t, hits, nil, 1)

	got, err := svc.Search(context.Background(), "ns-a", 1, mustCorrelationID(t),
		db.SearchInput{SessionID: 1}, []string{"alpha"}, bigBudget, VariantRankRound)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(got.Queries) != 1 || len(got.Queries[0].Hits) != 2 {
		t.Errorf("results = %+v; want 1 query / 2 hits", got)
	}
}

func TestSearch_PropagatesDALError(t *testing.T) {
	svc, _ := serviceReturningHits(t, nil, db.ErrSessionNotFound, 1)

	_, err := svc.Search(context.Background(), "ns-a", 1, mustCorrelationID(t),
		db.SearchInput{SessionID: 1}, []string{"x"}, bigBudget, VariantRankRound)
	if !errors.Is(err, db.ErrSessionNotFound) {
		t.Fatalf("err = %v; want ErrSessionNotFound", err)
	}
}

func TestSearch_PersistsCorrelationOnSuccess(t *testing.T) {
	corrID := mustCorrelationID(t)
	hits := []db.SearchHit{
		{MatchLayer: "primary"},
		{MatchLayer: "primary", SnippetFallback: true},
		{MatchLayer: "trigram"},
	}
	sources := db.NewMockSourcesRepo(t)
	sources.EXPECT().Search(mock.Anything, "ns-a", mock.Anything).Return(hits, nil).Once()
	corr := db.NewMockCorrelationsRepo(t)
	corr.EXPECT().Insert(
		mock.Anything, "ns-a", int64(1), corrID[:], "search",
		mock.MatchedBy(func(meta []byte) bool {
			var got map[string]any
			if err := json.Unmarshal(meta, &got); err != nil {
				return false
			}
			return got["hits_primary"] == float64(2) && got["hits_trigram"] == float64(1) &&
				got["hit_count"] == float64(3) && got["snippet_fallbacks"] == float64(1)
		}),
	).Return(nil).Once()
	dal := db.NewMockDAL(t)
	dal.EXPECT().Sources().Return(sources).Once()
	dal.EXPECT().Correlations().Return(corr).Once()
	svc := New(dal, logging.Nop())

	if _, err := svc.Search(context.Background(), "ns-a", 1, corrID,
		db.SearchInput{SessionID: 1}, []string{"alpha"}, bigBudget, VariantRankRound); err != nil {
		t.Fatalf("Search: %v", err)
	}
}

func TestSearch_CorrelationInsertFailureDoesNotBreakSearch(t *testing.T) {
	hits := []db.SearchHit{{MatchLayer: "primary"}}
	sources := db.NewMockSourcesRepo(t)
	sources.EXPECT().Search(mock.Anything, "ns-a", mock.Anything).Return(hits, nil).Once()
	corr := db.NewMockCorrelationsRepo(t)
	corr.EXPECT().Insert(
		mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything,
	).Return(errors.New("correlations table dropped")).Once()
	dal := db.NewMockDAL(t)
	dal.EXPECT().Sources().Return(sources).Once()
	dal.EXPECT().Correlations().Return(corr).Once()
	svc := New(dal, logging.Nop())

	got, err := svc.Search(context.Background(), "ns-a", 1, mustCorrelationID(t),
		db.SearchInput{SessionID: 1}, []string{"alpha"}, bigBudget, VariantRankRound)
	if err != nil {
		t.Fatalf("Search returned err %v; capture failure must not bubble", err)
	}
	if len(got.Queries) != 1 {
		t.Errorf("results len = %d; want 1", len(got.Queries))
	}
}

func TestSearch_PopulatesBudgetResult(t *testing.T) {
	hits := []db.SearchHit{
		{Title: "t1", Snippet: "s1", Source: "out", MatchLayer: "primary"},
	}
	svc, _ := serviceReturningHits(t, hits, nil, 1)

	got, err := svc.Search(context.Background(), "ns-a", 1, mustCorrelationID(t),
		db.SearchInput{SessionID: 1}, []string{"alpha"}, bigBudget, VariantRankRound)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if got.BudgetBytes != bigBudget {
		t.Errorf("BudgetBytes = %d; want echoed %d", got.BudgetBytes, bigBudget)
	}
	if got.Allocator != VariantRankRound {
		t.Errorf("Allocator = %q; want rank-round", got.Allocator)
	}
	if got.BudgetExceeded {
		t.Errorf("BudgetExceeded = true; a fitting result must not flag exceeded")
	}
	if got.ByteBudgetUsed <= 0 {
		t.Errorf("ByteBudgetUsed = %d; want > 0 for a returned hit", got.ByteBudgetUsed)
	}
}

func TestSearch_CorrelationMetaCarriesAllocatorAndBudget(t *testing.T) {
	corrID := mustCorrelationID(t)
	hits := []db.SearchHit{{Title: "t1", Snippet: "some snippet text", Source: "out", MatchLayer: "primary"}}
	sources := db.NewMockSourcesRepo(t)
	sources.EXPECT().Search(mock.Anything, "ns-a", mock.Anything).Return(hits, nil).Once()
	corr := db.NewMockCorrelationsRepo(t)
	corr.EXPECT().Insert(
		mock.Anything, "ns-a", int64(1), corrID[:], "search",
		mock.MatchedBy(func(meta []byte) bool {
			var got map[string]any
			if err := json.Unmarshal(meta, &got); err != nil {
				return false
			}
			return got["allocator"] == "equal-budget" && got["budget_exceeded"] == true &&
				got["byte_budget_used"] == float64(0) && got["results_omitted"] == float64(1)
		}),
	).Return(nil).Once()
	dal := db.NewMockDAL(t)
	dal.EXPECT().Sources().Return(sources).Once()
	dal.EXPECT().Correlations().Return(corr).Once()
	svc := New(dal, logging.Nop())

	if _, err := svc.Search(context.Background(), "ns-a", 1, corrID,
		db.SearchInput{SessionID: 1}, []string{"alpha"}, 1, VariantEqualBudget); err != nil {
		t.Fatalf("Search: %v", err)
	}
}

func TestSearch_HitBreakdownOverIncludedHits(t *testing.T) {
	corrID := mustCorrelationID(t)
	first := db.SearchHit{Title: "t1", Snippet: "s", Source: "o", MatchLayer: "primary"}
	second := db.SearchHit{Title: "t2", Snippet: "longer snippet body", Source: "o", MatchLayer: "primary"}
	firstSize, err := wireSize(first)
	if err != nil {
		t.Fatalf("wireSize: %v", err)
	}
	hits := []db.SearchHit{first, second}
	sources := db.NewMockSourcesRepo(t)
	sources.EXPECT().Search(mock.Anything, "ns-a", mock.Anything).Return(hits, nil).Once()
	corr := db.NewMockCorrelationsRepo(t)
	corr.EXPECT().Insert(
		mock.Anything, "ns-a", int64(1), corrID[:], "search",
		mock.MatchedBy(func(meta []byte) bool {
			var got map[string]any
			if err := json.Unmarshal(meta, &got); err != nil {
				return false
			}
			return got["hit_count"] == float64(1) && got["hits_primary"] == float64(1) &&
				got["results_omitted"] == float64(1)
		}),
	).Return(nil).Once()
	dal := db.NewMockDAL(t)
	dal.EXPECT().Sources().Return(sources).Once()
	dal.EXPECT().Correlations().Return(corr).Once()
	svc := New(dal, logging.Nop())

	got, err := svc.Search(context.Background(), "ns-a", 1, corrID,
		db.SearchInput{SessionID: 1}, []string{"alpha"}, firstSize, VariantRankRound)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(got.Queries[0].Hits) != 1 || got.Queries[0].BudgetOmitted != 1 {
		t.Errorf("query result = %+v; want 1 hit / 1 omitted", got.Queries[0])
	}
}

func assertIntentZeroMatch(t *testing.T, queries []string, misses map[string]bool, want int) {
	t.Helper()
	corrID := mustCorrelationID(t)
	hit := []db.SearchHit{{Title: "t", Snippet: "s", Source: "out", MatchLayer: "primary"}}
	sources := db.NewMockSourcesRepo(t)
	for _, q := range queries {
		ret := hit
		if misses[q] {
			ret = nil
		}
		sources.EXPECT().Search(mock.Anything, "ns-a",
			mock.MatchedBy(func(in db.SearchInput) bool { return in.Query == q })).
			Return(ret, nil).Once()
	}
	corr := db.NewMockCorrelationsRepo(t)
	corr.EXPECT().Insert(
		mock.Anything, "ns-a", int64(1), corrID[:], "search",
		mock.MatchedBy(func(meta []byte) bool {
			var got map[string]any
			if err := json.Unmarshal(meta, &got); err != nil {
				return false
			}
			return got["intent_zero_match"] == float64(want)
		}),
	).Return(nil).Once()
	dal := db.NewMockDAL(t)
	dal.EXPECT().Sources().Return(sources).Maybe()
	dal.EXPECT().Correlations().Return(corr).Maybe()
	svc := New(dal, logging.Nop())

	if _, err := svc.Search(context.Background(), "ns-a", 1, corrID,
		db.SearchInput{SessionID: 1}, queries, bigBudget, VariantRankRound); err != nil {
		t.Fatalf("Search: %v", err)
	}
}

func TestSearch_IntentZeroMatchCountsMissedQueries(t *testing.T) {
	assertIntentZeroMatch(t,
		[]string{"hit1", "miss1", "hit2", "miss2"},
		map[string]bool{"miss1": true, "miss2": true}, 2)
}

func TestSearch_IntentZeroMatchAllHitIsZero(t *testing.T) {
	assertIntentZeroMatch(t, []string{"hit1", "hit2"}, nil, 0)
}

func TestSearch_IntentZeroMatchAllMissEqualsQueryCount(t *testing.T) {
	assertIntentZeroMatch(t,
		[]string{"m1", "m2", "m3"},
		map[string]bool{"m1": true, "m2": true, "m3": true}, 3)
}

func TestNormalizeRelevance_ResponseWideBandsAndDifferentiation(t *testing.T) {
	perQuery := [][]db.SearchHit{
		{
			{MatchLayer: "primary", MatchesAll: true, Relevance: 10.0},
			{MatchLayer: "primary", MatchesAll: true, Relevance: 5.0},
			{MatchLayer: "primary", Relevance: 50.0},
			{MatchLayer: "trigram", Relevance: 0.9},
		},
		{
			{MatchLayer: "primary", MatchesAll: true, Relevance: 7.0},
			{MatchLayer: "trigram", Relevance: 0.3},
		},
	}
	normalizeRelevance(perQuery)

	q0Top, q1Top := perQuery[0][0].Relevance, perQuery[1][0].Relevance
	if q0Top != 1.0 {
		t.Errorf("group-max AND hit = %v; want 1.0", q0Top)
	}
	if q1Top == q0Top {
		t.Errorf("both query tops normalized to %v; per-query degeneration", q0Top)
	}

	minAND := 1.0
	maxOR, maxTrigram := 0.0, 0.0
	for qi, hits := range perQuery {
		for i, h := range hits {
			switch {
			case h.MatchLayer == "primary" && h.MatchesAll:
				if h.Relevance < matchesAllRelevanceFloor || h.Relevance > 1.0 {
					t.Errorf("perQuery[%d][%d] AND relevance %v outside [%v, 1.0]", qi, i, h.Relevance, matchesAllRelevanceFloor)
				}
				if h.Relevance < minAND {
					minAND = h.Relevance
				}
			case h.MatchLayer == "primary":
				if h.Relevance < primaryRelevanceFloor || h.Relevance > primaryRelevanceCeil {
					t.Errorf("perQuery[%d][%d] OR relevance %v outside [%v, %v]", qi, i, h.Relevance, primaryRelevanceFloor, primaryRelevanceCeil)
				}
				if h.Relevance > maxOR {
					maxOR = h.Relevance
				}
			case h.MatchLayer == "trigram":
				if h.Relevance < trigramRelevanceFloor || h.Relevance > trigramRelevanceCeil {
					t.Errorf("perQuery[%d][%d] trigram relevance %v outside [%v, %v]", qi, i, h.Relevance, trigramRelevanceFloor, trigramRelevanceCeil)
				}
				if h.Relevance > maxTrigram {
					maxTrigram = h.Relevance
				}
			}
		}
	}
	if maxOR >= minAND {
		t.Errorf("highest OR hit %v >= lowest AND hit %v; AND tier not preserved in value space", maxOR, minAND)
	}
	if maxTrigram >= primaryRelevanceFloor {
		t.Errorf("highest trigram hit %v reaches the primary band", maxTrigram)
	}
}

func TestNormalizeRelevance_AllEqualGetsBandHigh(t *testing.T) {
	perQuery := [][]db.SearchHit{
		{
			{MatchLayer: "primary", MatchesAll: true, Relevance: 3.3},
			{MatchLayer: "primary", Relevance: 2.2},
			{MatchLayer: "trigram", Relevance: 0.4},
		},
		{
			{MatchLayer: "primary", MatchesAll: true, Relevance: 3.3},
			{MatchLayer: "primary", Relevance: 2.2},
			{MatchLayer: "trigram", Relevance: 0.4},
		},
	}
	normalizeRelevance(perQuery)
	for qi, hits := range perQuery {
		for i, h := range hits {
			switch {
			case h.MatchLayer == "primary" && h.MatchesAll:
				if h.Relevance != 1.0 {
					t.Errorf("perQuery[%d][%d] all-equal AND = %v; want 1.0", qi, i, h.Relevance)
				}
			case h.MatchLayer == "primary":
				if h.Relevance != primaryRelevanceCeil {
					t.Errorf("perQuery[%d][%d] all-equal OR = %v; want %v", qi, i, h.Relevance, primaryRelevanceCeil)
				}
			case h.MatchLayer == "trigram":
				if h.Relevance != trigramRelevanceCeil {
					t.Errorf("perQuery[%d][%d] all-equal trigram = %v; want %v", qi, i, h.Relevance, trigramRelevanceCeil)
				}
			}
		}
	}
}

func TestKnapsackPrefersMatchesAllUnderTightBudget(t *testing.T) {
	perQuery := [][]db.SearchHit{{
		{MatchLayer: "primary", MatchesAll: true, Snippet: "and-hit", Relevance: 2.1},
		{MatchLayer: "primary", Snippet: "or-hit", Relevance: 5.2},
	}}
	normalizeRelevance(perQuery)

	cs := make([]candidate, 0, 2)
	for _, h := range perQuery[0] {
		cs = append(cs, candidate{hit: h, size: 10, relevance: h.Relevance})
	}
	out := knapsackGreedy{}.Allocate(allocInput{
		queries:    []string{"q"},
		candidates: [][]candidate{cs},
		budget:     10,
	})
	if len(out.included[0]) != 1 || !out.included[0][0].hit.MatchesAll {
		t.Fatalf("included = %+v; want the AND hit despite the OR hit's higher raw score", out.included[0])
	}
}
