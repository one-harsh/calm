// Copyright 2026 The CALM Authors
// SPDX-License-Identifier: Apache-2.0

package search

import (
	"context"
	"errors"
	"testing"

	logging "github.com/one-harsh/context-logging"
	"github.com/stretchr/testify/mock"

	"github.com/one-harsh/calm/internal/db"
)

func TestSearch_ParallelFanOutPreservesOrder(t *testing.T) {
	sources := db.NewMockSourcesRepo(t)
	sources.EXPECT().Search(mock.Anything, "ns-a", mock.Anything).
		RunAndReturn(func(_ context.Context, _ string, in db.SearchInput) ([]db.SearchHit, error) {
			return []db.SearchHit{{Title: in.Query, Snippet: in.Query, Source: "o", MatchLayer: "primary"}}, nil
		})
	corr := db.NewMockCorrelationsRepo(t)
	corr.EXPECT().Insert(mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything).
		Return(nil).Maybe()
	dal := db.NewMockDAL(t)
	dal.EXPECT().Sources().Return(sources).Maybe()
	dal.EXPECT().Correlations().Return(corr).Maybe()
	svc := New(dal, logging.Nop())

	queries := []string{"alpha", "beta", "alpha", "gamma"}
	got, err := svc.Search(context.Background(), "ns-a", 1, mustCorrelationID(t),
		db.SearchInput{SessionID: 1}, queries, bigBudget, VariantRankRound)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(got.Queries) != len(queries) {
		t.Fatalf("query results = %d; want %d", len(got.Queries), len(queries))
	}
	for i, q := range queries {
		if got.Queries[i].Query != q {
			t.Errorf("position %d: query = %q; want %q (order not preserved)", i, got.Queries[i].Query, q)
		}
		if len(got.Queries[i].Hits) != 1 || got.Queries[i].Hits[0].Title != q {
			t.Errorf("position %d: hit = %+v; want title %q (duplicate collapsed?)", i, got.Queries[i].Hits, q)
		}
	}
}

func TestSearch_FirstErrorFailsFast(t *testing.T) {
	sentinel := errors.New("query failed")
	sources := db.NewMockSourcesRepo(t)
	sources.EXPECT().Search(mock.Anything, "ns-a", mock.Anything).
		RunAndReturn(func(_ context.Context, _ string, in db.SearchInput) ([]db.SearchHit, error) {
			if in.Query == "boom" {
				return nil, sentinel
			}
			return []db.SearchHit{}, nil
		}).Maybe()
	dal := db.NewMockDAL(t)
	dal.EXPECT().Sources().Return(sources).Maybe()
	svc := New(dal, logging.Nop())

	_, err := svc.Search(context.Background(), "ns-a", 1, mustCorrelationID(t),
		db.SearchInput{SessionID: 1}, []string{"ok", "boom", "ok"}, bigBudget, VariantRankRound)
	if !errors.Is(err, sentinel) {
		t.Fatalf("err = %v; want the fan-out sentinel", err)
	}
}
