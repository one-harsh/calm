// Copyright 2026 The CALM Authors
// SPDX-License-Identifier: Apache-2.0

package ingest

import (
	"context"
	"encoding/json"

	"github.com/google/uuid"
	logging "github.com/one-harsh/context-logging"

	"github.com/one-harsh/calm/internal/db"
	"github.com/one-harsh/calm/internal/obs"
)

const (
	summaryCap = 50
	previewMax = 200

	requestTypeIngest = "ingest"
)

type Service struct {
	store  db.DAL
	logger *logging.Logger
}

func New(store db.DAL, logger *logging.Logger) *Service {
	return &Service{store: store, logger: logger}
}

type Input struct {
	Source      string
	Content     string
	Format      string // "" = auto-detect
	ContentType string // "" = "prose"
}

type Section struct {
	Title   string
	Preview string
}

type Result struct {
	Source           string
	Created          bool
	SectionsIndexed  int
	SectionsTotal    int
	SummaryTruncated bool
	Summary          []Section
	DistinctiveTerms []string
}

func (s *Service) Ingest(ctx context.Context, namespace string, sessionID int64, correlationID uuid.UUID, in Input) (Result, error) {
	if in.Source == "" {
		return Result{}, db.ErrSourceRequired
	}
	chunks := chunk(in.Source, in.Content, in.Format, in.ContentType)

	var created bool
	err := s.store.WithTx(ctx, func(r db.Repos) error {
		srcID, c, err := r.Sources.Upsert(ctx, namespace, sessionID, in.Source)
		if err != nil {
			return err
		}
		created = c
		if err := r.Chunks.DeleteForSource(ctx, srcID); err != nil {
			return err
		}
		// Empty content is valid: the delete above leaves the source with no chunks.
		return r.Chunks.Insert(ctx, srcID, chunks)
	})
	if err != nil {
		return Result{}, err
	}

	total := len(chunks)
	indexed := total
	truncated := false
	if indexed > summaryCap {
		indexed = summaryCap
		truncated = true
	}
	summary := make([]Section, indexed)
	for i := 0; i < indexed; i++ {
		summary[i] = Section{Title: chunks[i].Title, Preview: preview(chunks[i].Content)}
	}

	// HLD-DEVIATION: distinctive_terms requires the vocabulary doc_freq table
	// (HLD ingest section); smoke returns none.
	result := Result{
		Source:           in.Source,
		Created:          created,
		SectionsIndexed:  indexed,
		SectionsTotal:    total,
		SummaryTruncated: truncated,
		Summary:          summary,
		DistinctiveTerms: []string{},
	}
	s.captureCorrelation(ctx, namespace, sessionID, correlationID, result)
	return result, nil
}

func (s *Service) captureCorrelation(ctx context.Context, namespace string, sessionID int64, correlationID uuid.UUID, result Result) {
	meta, err := json.Marshal(map[string]any{
		"sections_indexed":  result.SectionsIndexed,
		"sections_total":    result.SectionsTotal,
		"summary_truncated": result.SummaryTruncated,
	})
	if err != nil {
		s.logger.WithContext(ctx).Warn("correlation marshal failed",
			obs.RequestType(requestTypeIngest), logging.ErrorField(err))
		return
	}
	if err := s.store.Correlations().Insert(ctx, namespace, sessionID, correlationID[:], requestTypeIngest, meta); err != nil {
		s.logger.WithContext(ctx).Warn("correlation insert failed",
			obs.RequestType(requestTypeIngest), logging.ErrorField(err))
	}
}

func (s *Service) ListSources(ctx context.Context, namespace string, sessionID int64) ([]db.SourceSummary, error) {
	return s.store.Sources().List(ctx, namespace, sessionID)
}
