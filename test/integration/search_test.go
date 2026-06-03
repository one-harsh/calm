// Copyright 2026 The CALM Authors
// SPDX-License-Identifier: Apache-2.0

package integration

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/one-harsh/calm/internal/db"
)

func seedSearchCorpus(t *testing.T, store *db.Store, namespace string, sessionID int64) {
	t.Helper()
	if _, err := store.Sources().Index(context.Background(), namespace, db.IndexInput{
		SessionID: sessionID, Source: "build.log",
		Chunks: []db.Chunk{
			{Title: "compile step", Content: "the build failed with a fatal linker error", ContentType: "prose"},
			{Title: "test step", Content: "all unit tests passed cleanly", ContentType: "prose"},
		},
	}); err != nil {
		t.Fatalf("Index build.log: %v", err)
	}
	if _, err := store.Sources().Index(context.Background(), namespace, db.IndexInput{
		SessionID: sessionID, Source: "main.go",
		Chunks: []db.Chunk{
			{Title: "handler", Content: "func handleLinkerError() { return }", ContentType: "code"},
		},
	}); err != nil {
		t.Fatalf("Index main.go: %v", err)
	}
}

func TestSearch_MatchInContentReturnsHitWithSnippet(t *testing.T) {
	store, sqlDB, teardown := openConcreteStore(t)
	defer teardown()
	seedClient(t, sqlDB, "ns-a", db.DefaultClient)
	sess := seedSession(t, sqlDB, "ns-a", db.DefaultClient, 60)
	seedSearchCorpus(t, store, "ns-a", sess.ID)

	got, err := store.Sources().Search(context.Background(), "ns-a", db.SearchInput{
		SessionID: sess.ID, Queries: []string{"linker"},
	})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(got) != 1 || got[0].Query != "linker" {
		t.Fatalf("results = %+v; want one result for \"linker\"", got)
	}
	hits := got[0].Hits
	if len(hits) == 0 {
		t.Fatal("want at least one hit for \"linker\"")
	}
	for _, h := range hits {
		if h.MatchLayer != "primary" {
			t.Errorf("match_layer = %q; want primary", h.MatchLayer)
		}
		if !strings.Contains(strings.ToLower(h.Snippet), "linker") {
			t.Errorf("snippet %q does not contain the query term", h.Snippet)
		}
	}
}

func TestSearch_MatchInTitleOnly(t *testing.T) {
	store, sqlDB, teardown := openConcreteStore(t)
	defer teardown()
	seedClient(t, sqlDB, "ns-a", db.DefaultClient)
	sess := seedSession(t, sqlDB, "ns-a", db.DefaultClient, 60)
	if _, err := store.Sources().Index(context.Background(), "ns-a", db.IndexInput{
		SessionID: sess.ID, Source: "s",
		Chunks: []db.Chunk{{Title: "deployment guide", Content: "step one is to provision the cluster", ContentType: "prose"}},
	}); err != nil {
		t.Fatalf("Index: %v", err)
	}

	got, err := store.Sources().Search(context.Background(), "ns-a", db.SearchInput{
		SessionID: sess.ID, Queries: []string{"deployment"},
	})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(got[0].Hits) != 1 {
		t.Fatalf("want one hit matched on title; got %+v", got[0].Hits)
	}
	// Title-only match → snippet is a leading window of the content.
	if !strings.Contains(got[0].Hits[0].Snippet, "provision") {
		t.Errorf("snippet %q; want a content window", got[0].Hits[0].Snippet)
	}
}

func TestSearch_SourceFilterScopes(t *testing.T) {
	store, sqlDB, teardown := openConcreteStore(t)
	defer teardown()
	seedClient(t, sqlDB, "ns-a", db.DefaultClient)
	sess := seedSession(t, sqlDB, "ns-a", db.DefaultClient, 60)
	seedSearchCorpus(t, store, "ns-a", sess.ID)

	got, err := store.Sources().Search(context.Background(), "ns-a", db.SearchInput{
		SessionID: sess.ID, Queries: []string{"linker"}, Source: "main.go",
	})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	for _, h := range got[0].Hits {
		if h.Source != "main.go" {
			t.Errorf("hit from source %q; want only main.go", h.Source)
		}
	}
	if len(got[0].Hits) == 0 {
		t.Error("want the main.go hit for \"linker\"")
	}
}

func TestSearch_LimitCapsHits(t *testing.T) {
	store, sqlDB, teardown := openConcreteStore(t)
	defer teardown()
	seedClient(t, sqlDB, "ns-a", db.DefaultClient)
	sess := seedSession(t, sqlDB, "ns-a", db.DefaultClient, 60)
	chunks := make([]db.Chunk, 5)
	for i := range chunks {
		chunks[i] = db.Chunk{Title: "t", Content: "needle in the haystack", ContentType: "prose"}
	}
	if _, err := store.Sources().Index(context.Background(), "ns-a", db.IndexInput{
		SessionID: sess.ID, Source: "s", Chunks: chunks,
	}); err != nil {
		t.Fatalf("Index: %v", err)
	}

	got, err := store.Sources().Search(context.Background(), "ns-a", db.SearchInput{
		SessionID: sess.ID, Queries: []string{"needle"}, Limit: 2,
	})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(got[0].Hits) != 2 {
		t.Errorf("hits = %d; want 2 (limit)", len(got[0].Hits))
	}
}

func TestSearch_MultiQueryResultsPerQueryInOrder(t *testing.T) {
	store, sqlDB, teardown := openConcreteStore(t)
	defer teardown()
	seedClient(t, sqlDB, "ns-a", db.DefaultClient)
	sess := seedSession(t, sqlDB, "ns-a", db.DefaultClient, 60)
	seedSearchCorpus(t, store, "ns-a", sess.ID)

	got, err := store.Sources().Search(context.Background(), "ns-a", db.SearchInput{
		SessionID: sess.ID, Queries: []string{"linker", "nonexistent-term"},
	})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("results = %d; want 2 (one per query)", len(got))
	}
	if got[0].Query != "linker" || got[1].Query != "nonexistent-term" {
		t.Errorf("query order = [%q, %q]; want [linker, nonexistent-term]", got[0].Query, got[1].Query)
	}
	if len(got[0].Hits) == 0 {
		t.Error("first query should have hits")
	}
	if len(got[1].Hits) != 0 {
		t.Errorf("no-match query hits = %d; want 0", len(got[1].Hits))
	}
}

func TestSearch_CrossNamespaceMapsToNotFound(t *testing.T) {
	store, sqlDB, teardown := openConcreteStore(t)
	defer teardown()
	seedClient(t, sqlDB, "ns-a", db.DefaultClient)
	sess := seedSession(t, sqlDB, "ns-a", db.DefaultClient, 60)
	seedSearchCorpus(t, store, "ns-a", sess.ID)

	_, err := store.Sources().Search(context.Background(), "ns-b", db.SearchInput{
		SessionID: sess.ID, Queries: []string{"linker"},
	})
	if !errors.Is(err, db.ErrSessionNotFound) {
		t.Fatalf("err = %v; want ErrSessionNotFound", err)
	}
}

func TestSearch_SessionIsolationWithinNamespace(t *testing.T) {
	store, sqlDB, teardown := openConcreteStore(t)
	defer teardown()
	seedClient(t, sqlDB, "ns-a", db.DefaultClient)
	sessA := seedSession(t, sqlDB, "ns-a", db.DefaultClient, 60)
	sessB := seedSession(t, sqlDB, "ns-a", db.DefaultClient, 60)
	seedSearchCorpus(t, store, "ns-a", sessA.ID)

	got, err := store.Sources().Search(context.Background(), "ns-a", db.SearchInput{
		SessionID: sessB.ID, Queries: []string{"linker"},
	})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(got[0].Hits) != 0 {
		t.Errorf("session B sees %+v; want none (session isolation)", got[0].Hits)
	}
}

func TestSearch_ValidationErrors(t *testing.T) {
	store, sqlDB, teardown := openConcreteStore(t)
	defer teardown()
	seedClient(t, sqlDB, "ns-a", db.DefaultClient)
	sess := seedSession(t, sqlDB, "ns-a", db.DefaultClient, 60)

	if _, err := store.Sources().Search(context.Background(), "ns-a", db.SearchInput{
		SessionID: sess.ID, Queries: nil,
	}); !errors.Is(err, db.ErrQueryRequired) {
		t.Errorf("empty queries err = %v; want ErrQueryRequired", err)
	}
	// An empty query string must be rejected at the DAL (it would otherwise
	// match every chunk), not silently relied on HTTP validation to catch.
	if _, err := store.Sources().Search(context.Background(), "ns-a", db.SearchInput{
		SessionID: sess.ID, Queries: []string{"ok", ""},
	}); !errors.Is(err, db.ErrQueryRequired) {
		t.Errorf("empty query string err = %v; want ErrQueryRequired", err)
	}
	if _, err := store.Sources().Search(context.Background(), "ns-a", db.SearchInput{
		SessionID: sess.ID, Queries: []string{"x"}, Limit: -1,
	}); !errors.Is(err, db.ErrInvalidLimit) {
		t.Errorf("negative limit err = %v; want ErrInvalidLimit", err)
	}
}
