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

func mustCorrelationID(t *testing.T) uuid.UUID {
	t.Helper()
	id, err := uuid.NewV7()
	if err != nil {
		t.Fatalf("uuid.NewV7: %v", err)
	}
	return id
}

func serviceReturning(t *testing.T, results []db.SearchResult, err error) (*Service, *db.MockCorrelationsRepo) {
	t.Helper()
	sources := db.NewMockSourcesRepo(t)
	sources.EXPECT().Search(mock.Anything, "ns-a", mock.Anything).Return(results, err).Once()
	corr := db.NewMockCorrelationsRepo(t)
	corr.EXPECT().Insert(
		mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything,
	).Return(nil).Maybe()
	dal := db.NewMockDAL(t)
	dal.EXPECT().Sources().Return(sources).Once()
	dal.EXPECT().Correlations().Return(corr).Maybe()
	return New(dal, logging.Nop()), corr
}

func TestSearch_HappyPathReturnsResults(t *testing.T) {
	results := []db.SearchResult{{
		Query: "alpha",
		Hits: []db.SearchHit{
			{Title: "t1", Snippet: "s1", Source: "out", MatchLayer: "primary"},
			{Title: "t2", Snippet: "s2", Source: "out", MatchLayer: "primary"},
		},
	}}
	svc, _ := serviceReturning(t, results, nil)

	got, err := svc.Search(context.Background(), "ns-a", 1, mustCorrelationID(t),
		db.SearchInput{SessionID: 1, Queries: []string{"alpha"}})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(got) != 1 || len(got[0].Hits) != 2 {
		t.Errorf("results = %+v; want 1 query / 2 hits", got)
	}
}

func TestSearch_PropagatesDALError(t *testing.T) {
	svc, _ := serviceReturning(t, nil, db.ErrSessionNotFound)

	_, err := svc.Search(context.Background(), "ns-a", 1, mustCorrelationID(t),
		db.SearchInput{SessionID: 1, Queries: []string{"x"}})
	if !errors.Is(err, db.ErrSessionNotFound) {
		t.Fatalf("err = %v; want ErrSessionNotFound", err)
	}
}

func TestSearch_PersistsCorrelationOnSuccess(t *testing.T) {
	corrID := mustCorrelationID(t)
	results := []db.SearchResult{{
		Query: "alpha",
		Hits: []db.SearchHit{
			{MatchLayer: "primary"},
			{MatchLayer: "primary", SnippetFallback: true},
			{MatchLayer: "trigram"},
		},
	}}
	sources := db.NewMockSourcesRepo(t)
	sources.EXPECT().Search(mock.Anything, "ns-a", mock.Anything).Return(results, nil).Once()
	corr := db.NewMockCorrelationsRepo(t)
	corr.EXPECT().Insert(
		mock.Anything, "ns-a", int64(1), corrID[:], "search",
		mock.MatchedBy(func(meta []byte) bool {
			var got map[string]any
			if err := json.Unmarshal(meta, &got); err != nil {
				return false
			}
			// hits_primary=2, hits_trigram=1, hit_count=3, snippet_fallbacks=1 → flat fields.
			return got["hits_primary"] == float64(2) && got["hits_trigram"] == float64(1) &&
				got["hit_count"] == float64(3) && got["snippet_fallbacks"] == float64(1)
		}),
	).Return(nil).Once()
	dal := db.NewMockDAL(t)
	dal.EXPECT().Sources().Return(sources).Once()
	dal.EXPECT().Correlations().Return(corr).Once()
	svc := New(dal, logging.Nop())

	if _, err := svc.Search(context.Background(), "ns-a", 1, corrID,
		db.SearchInput{SessionID: 1, Queries: []string{"alpha"}}); err != nil {
		t.Fatalf("Search: %v", err)
	}
}

func TestSearch_CorrelationInsertFailureDoesNotBreakSearch(t *testing.T) {
	results := []db.SearchResult{{Query: "alpha", Hits: []db.SearchHit{{MatchLayer: "primary"}}}}
	sources := db.NewMockSourcesRepo(t)
	sources.EXPECT().Search(mock.Anything, "ns-a", mock.Anything).Return(results, nil).Once()
	corr := db.NewMockCorrelationsRepo(t)
	corr.EXPECT().Insert(
		mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything,
	).Return(errors.New("correlations table dropped")).Once()
	dal := db.NewMockDAL(t)
	dal.EXPECT().Sources().Return(sources).Once()
	dal.EXPECT().Correlations().Return(corr).Once()
	svc := New(dal, logging.Nop())

	got, err := svc.Search(context.Background(), "ns-a", 1, mustCorrelationID(t),
		db.SearchInput{SessionID: 1, Queries: []string{"alpha"}})
	if err != nil {
		t.Fatalf("Search returned err %v; capture failure must not bubble", err)
	}
	if len(got) != 1 {
		t.Errorf("results len = %d; want 1", len(got))
	}
}
