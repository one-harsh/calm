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

const (
	requestTypeSearch = "search"

	matchLayerPrimary = "primary"
	matchLayerTrigram = "trigram"
)

type Service struct {
	store  db.DAL
	logger *logging.Logger
}

func New(store db.DAL, logger *logging.Logger) *Service {
	return &Service{store: store, logger: logger}
}

// Search runs the workload's query through the sources DAL and, on success,
// best-effort captures a correlations row keyed by correlationID. The search
// service is the seam where allocator pluggability eventually lives.
func (s *Service) Search(ctx context.Context, namespace string, sessionID int64, correlationID uuid.UUID, in db.SearchInput) ([]db.SearchResult, error) {
	results, err := s.store.Sources().Search(ctx, namespace, in)
	if err != nil {
		return nil, err
	}
	primary, trigram, total := hitBreakdown(results)
	if s.logger.Enabled(logging.DebugLevel) {
		s.logger.WithContext(ctx).Debug(
			"search executed",
			obs.SearchQueries(len(in.Queries)),
			obs.SearchHitsTotal(total),
			obs.SearchHitsPrimary(primary),
			obs.SearchHitsTrigram(trigram),
		)
	}
	s.captureCorrelation(ctx, namespace, sessionID, correlationID, primary, trigram, total)
	return results, nil
}

func hitBreakdown(results []db.SearchResult) (primary, trigram, total int) {
	for _, r := range results {
		for _, h := range r.Hits {
			total++
			switch h.MatchLayer {
			case matchLayerPrimary:
				primary++
			case matchLayerTrigram:
				trigram++
			}
		}
	}
	return primary, trigram, total
}

func (s *Service) captureCorrelation(ctx context.Context, namespace string, sessionID int64, correlationID uuid.UUID, primary, trigram, total int) {
	meta, err := json.Marshal(map[string]any{
		"hits_primary": primary,
		"hits_trigram": trigram,
		"hit_count":    total,
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
