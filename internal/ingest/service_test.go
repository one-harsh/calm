// Copyright 2026 The CALM Authors
// SPDX-License-Identifier: Apache-2.0

package ingest

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/mock"

	"github.com/one-harsh/calm/internal/db"
)

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

func newMockService(t *testing.T) (*Service, *db.MockSourcesRepo) {
	t.Helper()
	sources := db.NewMockSourcesRepo(t)
	dal := db.NewMockDAL(t)
	dal.EXPECT().Sources().Return(sources)
	return New(dal), sources
}

func TestIngest_HappyBuildsSummary(t *testing.T) {
	svc, sources := newMockService(t)
	sources.EXPECT().Index(mock.Anything, "ns-a", mock.Anything).Return(true, nil).Once()

	res, err := svc.Ingest(context.Background(), "ns-a", 1, Input{Source: "out", Content: "alpha\n\nbeta"})
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
		t.Error("Created = false; want true (Index reported a fresh insert)")
	}
}

func TestIngest_ReindexReportsNotCreated(t *testing.T) {
	svc, sources := newMockService(t)
	sources.EXPECT().Index(mock.Anything, "ns-a", mock.Anything).Return(false, nil).Once()

	res, err := svc.Ingest(context.Background(), "ns-a", 1, Input{Source: "out", Content: "alpha"})
	if err != nil {
		t.Fatalf("Ingest: %v", err)
	}
	if res.Created {
		t.Error("Created = true; want false (Index reported an update)")
	}
}

func TestIngest_EmptyContentIndexesNothing(t *testing.T) {
	svc, sources := newMockService(t)
	// Index is still called (clears prior content) even with zero chunks.
	sources.EXPECT().Index(mock.Anything, "ns-a", mock.Anything).Return(true, nil).Once()

	res, err := svc.Ingest(context.Background(), "ns-a", 1, Input{Source: "out", Content: "   "})
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
	svc, sources := newMockService(t)
	sources.EXPECT().Index(mock.Anything, "ns-a", mock.Anything).Return(true, nil).Once()

	var sb strings.Builder
	for i := range 60 {
		if i > 0 {
			sb.WriteString("\n\n")
		}
		fmt.Fprintf(&sb, "paragraph %d", i)
	}
	res, err := svc.Ingest(context.Background(), "ns-a", 1, Input{Source: "big", Content: sb.String()})
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

func TestIngest_DALErrorPropagates(t *testing.T) {
	svc, sources := newMockService(t)
	sources.EXPECT().Index(mock.Anything, "ns-a", mock.Anything).Return(false, db.ErrSessionNotFound).Once()

	_, err := svc.Ingest(context.Background(), "ns-a", 1, Input{Source: "x", Content: "y"})
	if !errors.Is(err, db.ErrSessionNotFound) {
		t.Fatalf("err = %v; want ErrSessionNotFound", err)
	}
}
