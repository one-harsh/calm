// Copyright 2026 The CALM Authors
// SPDX-License-Identifier: Apache-2.0

package ingest

import (
	"context"

	"github.com/one-harsh/calm/internal/db"
)

const (
	summaryCap = 50
	previewMax = 200
)

type Service struct {
	store db.DAL
}

func New(store db.DAL) *Service {
	return &Service{store: store}
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
	SectionsIndexed  int
	SectionsTotal    int
	SummaryTruncated bool
	Summary          []Section
	DistinctiveTerms []string
}

func (s *Service) Ingest(ctx context.Context, namespace string, sessionID int64, in Input) (Result, error) {
	chunks := chunk(in.Source, in.Content, in.Format, in.ContentType)
	if err := s.store.Sources().Index(ctx, namespace, db.IndexInput{
		SessionID: sessionID,
		Source:    in.Source,
		Chunks:    chunks,
	}); err != nil {
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
	return Result{
		Source:           in.Source,
		SectionsIndexed:  indexed,
		SectionsTotal:    total,
		SummaryTruncated: truncated,
		Summary:          summary,
		DistinctiveTerms: []string{},
	}, nil
}

func (s *Service) ListSources(ctx context.Context, namespace string, sessionID int64) ([]db.SourceSummary, error) {
	return s.store.Sources().List(ctx, namespace, sessionID)
}
