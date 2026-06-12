// Copyright 2026 The CALM Authors
// SPDX-License-Identifier: Apache-2.0

package ingest

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
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

func TestChunk_MarkdownSplitsAtHeadings(t *testing.T) {
	content := "intro line\n\n# First\nbody one\n\n## Second\nbody two\n"
	got := chunk("src", content, "", "")
	if len(got) != 3 {
		t.Fatalf("got %d chunks; want 3: %+v", len(got), got)
	}
	if got[0].Title != "src" {
		t.Errorf("chunk[0].Title = %q; want preamble titled source", got[0].Title)
	}
	if got[1].Title != "First" || got[2].Title != "Second" {
		t.Errorf("heading titles = %q,%q; want First,Second", got[1].Title, got[2].Title)
	}
	for _, c := range got {
		if c.ContentType != "prose" {
			t.Errorf("content_type = %q; want prose default", c.ContentType)
		}
	}
}

func TestChunk_TextSplitsOnBlankLines(t *testing.T) {
	content := "para one\nstill one\n\npara two\n\n\npara three"
	got := chunk("src", content, "text", "")
	if len(got) != 3 {
		t.Fatalf("got %d chunks; want 3: %+v", len(got), got)
	}
	if got[0].Content != "para one\nstill one" {
		t.Errorf("chunk[0].Content = %q", got[0].Content)
	}
}

func TestChunk_EmptyContentNoChunks(t *testing.T) {
	if got := chunk("src", "   \n  ", "", ""); len(got) != 0 {
		t.Fatalf("got %+v; want zero chunks for whitespace-only content", got)
	}
}

func TestChunk_HonorsContentTypeHint(t *testing.T) {
	got := chunk("src", "hello world", "text", "code")
	if got[0].ContentType != "code" {
		t.Errorf("content_type = %q; want code", got[0].ContentType)
	}
}

type ingestMocks struct {
	sources      *db.MockSourcesRepo
	chunks       *db.MockChunksRepo
	vocabulary   *db.MockVocabularyRepo
	correlations *db.MockCorrelationsRepo
}

// newMockService wires the service over a MockDAL whose WithTx invokes the
// closure with mock repos, so tests set expectations on the composed ops.
// Correlations runs outside WithTx (best-effort capture); the mock allows
// any number of post-success Insert calls.
func newMockService(t *testing.T) (*Service, ingestMocks) {
	t.Helper()
	m := ingestMocks{
		sources:      db.NewMockSourcesRepo(t),
		chunks:       db.NewMockChunksRepo(t),
		vocabulary:   db.NewMockVocabularyRepo(t),
		correlations: db.NewMockCorrelationsRepo(t),
	}
	dal := db.NewMockDAL(t)
	dal.EXPECT().WithTx(mock.Anything, mock.Anything).RunAndReturn(
		func(_ context.Context, fn func(db.Repos) error) error {
			return fn(db.Repos{Sources: m.sources, Chunks: m.chunks, Vocabulary: m.vocabulary, Correlations: m.correlations})
		},
	).Maybe()
	dal.EXPECT().Correlations().Return(m.correlations).Maybe()
	m.correlations.EXPECT().Insert(
		mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything,
	).Return(nil).Maybe()
	return New(dal, logging.Nop()), m
}

// expectIndex sets the happy-path WithTx composition: upsert (namespace-scoped) →
// vocab decrement → delete → insert → vocab increment → vocab prune. Insert is
// always called (it no-ops on empty content).
func (m ingestMocks) expectIndex(sessionID int64, source string, created bool) {
	m.sources.EXPECT().Upsert(mock.Anything, "ns-a", sessionID, source).Return(int64(7), created, nil).Once()
	m.vocabulary.EXPECT().DecrementForSource(mock.Anything, int64(7)).Return(nil).Once()
	m.chunks.EXPECT().DeleteForSource(mock.Anything, int64(7)).Return(nil).Once()
	m.chunks.EXPECT().Insert(mock.Anything, int64(7), mock.Anything).Return(nil).Once()
	m.vocabulary.EXPECT().IncrementForSource(mock.Anything, int64(7)).Return(nil).Once()
	m.vocabulary.EXPECT().PruneZeros(mock.Anything, sessionID).Return(nil).Once()
}

func TestIngest_HappyBuildsSummary(t *testing.T) {
	svc, m := newMockService(t)
	m.expectIndex(1, "out", true)

	res, err := svc.Ingest(context.Background(), "ns-a", 1, mustCorrelationID(t), Input{Source: "out", Content: "alpha\n\nbeta"})
	if err != nil {
		t.Fatalf("Ingest: %v", err)
	}
	if res.Source != "out" {
		t.Errorf("Source = %q; want out", res.Source)
	}
	if res.SectionsTotal != 2 || res.SectionsIndexed != 2 {
		t.Errorf("total/indexed = %d/%d; want 2/2", res.SectionsTotal, res.SectionsIndexed)
	}
	if res.SummaryTruncated {
		t.Error("SummaryTruncated = true; want false")
	}
	if len(res.Summary) != 2 {
		t.Errorf("summary len = %d; want 2", len(res.Summary))
	}
	if res.DistinctiveTerms == nil || len(res.DistinctiveTerms) != 0 {
		t.Errorf("DistinctiveTerms = %#v; want non-nil empty slice", res.DistinctiveTerms)
	}
	if !res.Created {
		t.Error("Created = false; want true (Upsert reported a fresh insert)")
	}
}

func TestIngest_ReindexReportsNotCreated(t *testing.T) {
	svc, m := newMockService(t)
	m.expectIndex(1, "out", false)

	res, err := svc.Ingest(context.Background(), "ns-a", 1, mustCorrelationID(t), Input{Source: "out", Content: "alpha"})
	if err != nil {
		t.Fatalf("Ingest: %v", err)
	}
	if res.Created {
		t.Error("Created = true; want false (Upsert reported an update)")
	}
}

func TestIngest_EmptyContentIndexesNothing(t *testing.T) {
	svc, m := newMockService(t)
	// DeleteForSource is still called (clears prior content); Insert no-ops on zero chunks.
	m.expectIndex(1, "out", true)

	res, err := svc.Ingest(context.Background(), "ns-a", 1, mustCorrelationID(t), Input{Source: "out", Content: "   "})
	if err != nil {
		t.Fatalf("Ingest: %v", err)
	}
	if res.SectionsTotal != 0 || res.SectionsIndexed != 0 {
		t.Errorf("total/indexed = %d/%d; want 0/0", res.SectionsTotal, res.SectionsIndexed)
	}
	if len(res.Summary) != 0 {
		t.Errorf("summary len = %d; want 0", len(res.Summary))
	}
	if res.SummaryTruncated {
		t.Error("SummaryTruncated = true; want false")
	}
}

func TestIngest_TruncatesSummaryAt50(t *testing.T) {
	svc, m := newMockService(t)
	m.expectIndex(1, "big", true)

	var sb strings.Builder
	for i := range 60 {
		if i > 0 {
			sb.WriteString("\n\n")
		}
		fmt.Fprintf(&sb, "paragraph %d", i)
	}
	res, err := svc.Ingest(context.Background(), "ns-a", 1, mustCorrelationID(t), Input{Source: "big", Content: sb.String()})
	if err != nil {
		t.Fatalf("Ingest: %v", err)
	}
	if res.SectionsTotal != 60 {
		t.Errorf("SectionsTotal = %d; want 60", res.SectionsTotal)
	}
	if res.SectionsIndexed != 50 {
		t.Errorf("SectionsIndexed = %d; want 50", res.SectionsIndexed)
	}
	if !res.SummaryTruncated {
		t.Error("SummaryTruncated = false; want true")
	}
	if len(res.Summary) != 50 {
		t.Errorf("summary len = %d; want 50", len(res.Summary))
	}
}

func TestIngest_EmptySourceRejected(t *testing.T) {
	svc, _ := newMockService(t) // WithTx must NOT be reached
	_, err := svc.Ingest(context.Background(), "ns-a", 1, mustCorrelationID(t), Input{Source: "", Content: "y"})
	if !errors.Is(err, db.ErrSourceRequired) {
		t.Fatalf("err = %v; want ErrSourceRequired", err)
	}
}

func TestIngest_SessionNotFoundPropagates(t *testing.T) {
	svc, m := newMockService(t)
	m.sources.EXPECT().Upsert(mock.Anything, "ns-a", int64(1), "x").Return(int64(0), false, db.ErrSessionNotFound).Once()

	_, err := svc.Ingest(context.Background(), "ns-a", 1, mustCorrelationID(t), Input{Source: "x", Content: "y"})
	if !errors.Is(err, db.ErrSessionNotFound) {
		t.Fatalf("err = %v; want ErrSessionNotFound", err)
	}
}

func TestIngest_PersistsCorrelationOnSuccess(t *testing.T) {
	corrID := mustCorrelationID(t)
	m := ingestMocks{
		sources:      db.NewMockSourcesRepo(t),
		chunks:       db.NewMockChunksRepo(t),
		vocabulary:   db.NewMockVocabularyRepo(t),
		correlations: db.NewMockCorrelationsRepo(t),
	}
	dal := db.NewMockDAL(t)
	dal.EXPECT().WithTx(mock.Anything, mock.Anything).RunAndReturn(
		func(_ context.Context, fn func(db.Repos) error) error {
			return fn(db.Repos{Sources: m.sources, Chunks: m.chunks, Vocabulary: m.vocabulary, Correlations: m.correlations})
		},
	).Once()
	dal.EXPECT().Correlations().Return(m.correlations).Once()
	m.expectIndex(1, "out", true)
	m.correlations.EXPECT().Insert(
		mock.Anything, "ns-a", int64(1), corrID[:], "ingest",
		mock.MatchedBy(func(meta []byte) bool {
			var got map[string]any
			if err := json.Unmarshal(meta, &got); err != nil {
				return false
			}
			_, hasIndexed := got["sections_indexed"]
			_, hasTotal := got["sections_total"]
			_, hasTruncated := got["summary_truncated"]
			return hasIndexed && hasTotal && hasTruncated
		}),
	).Return(nil).Once()
	svc := New(dal, logging.Nop())

	if _, err := svc.Ingest(context.Background(), "ns-a", 1, corrID, Input{Source: "out", Content: "alpha"}); err != nil {
		t.Fatalf("Ingest: %v", err)
	}
}

func TestIngest_CorrelationInsertFailureDoesNotBreakIngest(t *testing.T) {
	m := ingestMocks{
		sources:      db.NewMockSourcesRepo(t),
		chunks:       db.NewMockChunksRepo(t),
		vocabulary:   db.NewMockVocabularyRepo(t),
		correlations: db.NewMockCorrelationsRepo(t),
	}
	dal := db.NewMockDAL(t)
	dal.EXPECT().WithTx(mock.Anything, mock.Anything).RunAndReturn(
		func(_ context.Context, fn func(db.Repos) error) error {
			return fn(db.Repos{Sources: m.sources, Chunks: m.chunks, Vocabulary: m.vocabulary, Correlations: m.correlations})
		},
	).Once()
	dal.EXPECT().Correlations().Return(m.correlations).Once()
	m.expectIndex(1, "out", true)
	m.correlations.EXPECT().Insert(
		mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything,
	).Return(errors.New("correlations table dropped")).Once()
	svc := New(dal, logging.Nop())

	res, err := svc.Ingest(context.Background(), "ns-a", 1, mustCorrelationID(t), Input{Source: "out", Content: "alpha"})
	if err != nil {
		t.Fatalf("Ingest returned err %v; capture failure must not bubble", err)
	}
	if res.Source != "out" {
		t.Errorf("Source = %q; want out", res.Source)
	}
}
