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
	seedIndexedSource(t, store, namespace, sessionID, "build.log", []db.Chunk{
		{Title: "compile step", Content: "the build failed with a fatal linker error", ContentType: "prose"},
		{Title: "test step", Content: "all unit tests passed cleanly", ContentType: "prose"},
	})
	seedIndexedSource(t, store, namespace, sessionID, "main.go", []db.Chunk{
		{Title: "handler", Content: "// linker recovery helper\nfunc handleLinkerError() { return }", ContentType: "code"},
	})
}

// Workload searches a session with indexed content; expects a hit whose snippet contains
// the query term verbatim and whose match_layer is primary (content-fidelity: snippets are
// exact indexed text, not paraphrased).
func TestSearch_MatchInContentReturnsHitWithSnippet(t *testing.T) {
	t.Parallel()
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

// A query that matches only a chunk title returns a hit whose snippet is a content window, not the title text.
func TestSearch_MatchInTitleOnly(t *testing.T) {
	t.Parallel()
	store, sqlDB, teardown := openConcreteStore(t)
	defer teardown()
	seedClient(t, sqlDB, "ns-a", db.DefaultClient)
	sess := seedSession(t, sqlDB, "ns-a", db.DefaultClient, 60)
	seedIndexedSource(t, store, "ns-a", sess.ID, "s",
		[]db.Chunk{{Title: "deployment guide", Content: "step one is to provision the cluster", ContentType: "prose"}})

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

// Supplying a source filter limits hits to chunks belonging to that source, ignoring matches in other sources.
func TestSearch_SourceFilterScopes(t *testing.T) {
	t.Parallel()
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

// A Limit of N returns at most N hits even when more matches exist.
func TestSearch_LimitCapsHits(t *testing.T) {
	t.Parallel()
	store, sqlDB, teardown := openConcreteStore(t)
	defer teardown()
	seedClient(t, sqlDB, "ns-a", db.DefaultClient)
	sess := seedSession(t, sqlDB, "ns-a", db.DefaultClient, 60)
	chunks := make([]db.Chunk, 5)
	for i := range chunks {
		chunks[i] = db.Chunk{Title: "t", Content: "needle in the haystack", ContentType: "prose"}
	}
	seedIndexedSource(t, store, "ns-a", sess.ID, "s", chunks)

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

// Multiple queries return one result element per query in input order; a query with no matches
// returns an empty hits list rather than being omitted from the response.
func TestSearch_MultiQueryResultsPerQueryInOrder(t *testing.T) {
	t.Parallel()
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

// Workload indexes content in namespace A then searches from namespace B; expects zero hits
// (namespace-isolation: cross-namespace data is invisible, not an error).
func TestSearch_CrossNamespaceIsInvisible(t *testing.T) {
	t.Parallel()
	store, sqlDB, teardown := openConcreteStore(t)
	defer teardown()
	seedClient(t, sqlDB, "ns-a", db.DefaultClient)
	sess := seedSession(t, sqlDB, "ns-a", db.DefaultClient, 60)
	seedSearchCorpus(t, store, "ns-a", sess.ID)

	got, err := store.Sources().Search(context.Background(), "ns-b", db.SearchInput{
		SessionID: sess.ID, Queries: []string{"linker"},
	})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(got) != 1 || len(got[0].Hits) != 0 {
		t.Fatalf("results = %+v; want one query result with zero hits (invisibility)", got)
	}
}

// Content indexed into session A is invisible to session B in the same namespace
// (session-isolation: each session sees only its own content).
func TestSearch_SessionIsolationWithinNamespace(t *testing.T) {
	t.Parallel()
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

// A prose query matches stemmed variants of its terms: "caching" retrieves a chunk whose
// content says "cached" — lexeme matching, not substring matching. The snippet is exact
// indexed text (content-fidelity), so it carries the stored form, not the query's.
func TestSearch_StemmedProseMatch(t *testing.T) {
	t.Parallel()
	store, sqlDB, teardown := openConcreteStore(t)
	defer teardown()
	seedClient(t, sqlDB, "ns-a", db.DefaultClient)
	sess := seedSession(t, sqlDB, "ns-a", db.DefaultClient, 60)
	seedIndexedSource(t, store, "ns-a", sess.ID, "notes.md",
		[]db.Chunk{{Title: "perf", Content: "the lookup result was cached for later reuse", ContentType: "prose"}})

	got, err := store.Sources().Search(context.Background(), "ns-a", db.SearchInput{
		SessionID: sess.ID, Queries: []string{"caching"},
	})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(got[0].Hits) != 1 {
		t.Fatalf("hits = %+v; want one stemmed match", got[0].Hits)
	}
	if !strings.Contains(got[0].Hits[0].Snippet, "cached") {
		t.Errorf("snippet = %q; want the stored form \"cached\"", got[0].Hits[0].Snippet)
	}
}

// Code-class matching is whole-identifier: "handleLinkerError" retrieves the code chunk
// (the code tokenizer keeps identifiers intact), while the partial identifier "LinkerError"
// finds nothing — substring retrieval is the secondary layer's job, not primary's.
func TestSearch_CodeIdentifierWholeTokenMatch(t *testing.T) {
	t.Parallel()
	store, sqlDB, teardown := openConcreteStore(t)
	defer teardown()
	seedClient(t, sqlDB, "ns-a", db.DefaultClient)
	sess := seedSession(t, sqlDB, "ns-a", db.DefaultClient, 60)
	seedIndexedSource(t, store, "ns-a", sess.ID, "main.go",
		[]db.Chunk{{Title: "handler", Content: "func handleLinkerError() { return }", ContentType: "code"}})

	got, err := store.Sources().Search(context.Background(), "ns-a", db.SearchInput{
		SessionID: sess.ID, Queries: []string{"handleLinkerError", "LinkerError"},
	})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(got[0].Hits) != 1 {
		t.Fatalf("whole-identifier hits = %+v; want one", got[0].Hits)
	}
	if !strings.Contains(got[0].Hits[0].Snippet, "handleLinkerError") {
		t.Errorf("snippet = %q; want the identifier verbatim", got[0].Hits[0].Snippet)
	}
	if len(got[1].Hits) != 0 {
		t.Errorf("partial-identifier hits = %+v; want none at the primary layer", got[1].Hits)
	}
}

// With the same term in chunk A's title and chunk B's content, A ranks first (title weighted
// 2.0 vs content 1.0). The content-match chunk is seeded first (lower id) so the ordering can
// only come from the score, not the id tie-break.
func TestSearch_TitleMatchOutranksContentMatch(t *testing.T) {
	t.Parallel()
	store, sqlDB, teardown := openConcreteStore(t)
	defer teardown()
	seedClient(t, sqlDB, "ns-a", db.DefaultClient)
	sess := seedSession(t, sqlDB, "ns-a", db.DefaultClient, 60)
	seedIndexedSource(t, store, "ns-a", sess.ID, "runbook.md", []db.Chunk{
		{Title: "deploy notes", Content: "the rollback completed without incident", ContentType: "prose"},
		{Title: "rollback procedure", Content: "step two restarts the service", ContentType: "prose"},
	})

	got, err := store.Sources().Search(context.Background(), "ns-a", db.SearchInput{
		SessionID: sess.ID, Queries: []string{"rollback"},
	})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(got[0].Hits) != 2 {
		t.Fatalf("hits = %+v; want both chunks", got[0].Hits)
	}
	if got[0].Hits[0].Title != "rollback procedure" {
		t.Errorf("first hit = %q; want the title match ranked above the content match", got[0].Hits[0].Title)
	}
}

// A two-term query tiers AND-matches above OR-matches: the chunk containing both terms ranks
// first, and the single-term chunk is still returned as fallback rather than dropped.
func TestSearch_AllTermsMatchOutranksPartial(t *testing.T) {
	t.Parallel()
	store, sqlDB, teardown := openConcreteStore(t)
	defer teardown()
	seedClient(t, sqlDB, "ns-a", db.DefaultClient)
	sess := seedSession(t, sqlDB, "ns-a", db.DefaultClient, 60)
	seedIndexedSource(t, store, "ns-a", sess.ID, "build.log", []db.Chunk{
		{Title: "warning", Content: "the linker emitted a deprecation warning", ContentType: "prose"},
		{Title: "failure", Content: "a fatal linker fault stopped the build", ContentType: "prose"},
	})

	got, err := store.Sources().Search(context.Background(), "ns-a", db.SearchInput{
		SessionID: sess.ID, Queries: []string{"fatal linker"},
	})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(got[0].Hits) != 2 {
		t.Fatalf("hits = %+v; want the AND match plus the partial match", got[0].Hits)
	}
	if got[0].Hits[0].Title != "failure" || got[0].Hits[1].Title != "warning" {
		t.Errorf("order = [%q, %q]; want the AND tier first: [failure, warning]",
			got[0].Hits[0].Title, got[0].Hits[1].Title)
	}
}

// One query whose term appears in both prose and code chunks returns hits from both tokenizer
// classes fused into a single ranked list, every hit labeled match_layer=primary — the two
// classes are one search surface, not two.
func TestSearch_MixedClassFusionSingleList(t *testing.T) {
	t.Parallel()
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
	sources := map[string]bool{}
	for _, h := range got[0].Hits {
		sources[h.Source] = true
		if h.MatchLayer != "primary" {
			t.Errorf("match_layer = %q; want primary for both classes", h.MatchLayer)
		}
	}
	if !sources["build.log"] || !sources["main.go"] {
		t.Errorf("hit sources = %v; want both the prose (build.log) and code (main.go) chunks", sources)
	}
}

// A query of only stopwords yields no prose lexemes and matches no code tokens; the result is
// an empty hit list, not an error — token-less queries degrade to no-match.
func TestSearch_AllStopwordQueryReturnsEmpty(t *testing.T) {
	t.Parallel()
	store, sqlDB, teardown := openConcreteStore(t)
	defer teardown()
	seedClient(t, sqlDB, "ns-a", db.DefaultClient)
	sess := seedSession(t, sqlDB, "ns-a", db.DefaultClient, 60)
	seedSearchCorpus(t, store, "ns-a", sess.ID)

	got, err := store.Sources().Search(context.Background(), "ns-a", db.SearchInput{
		SessionID: sess.ID, Queries: []string{"the of and"},
	})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(got[0].Hits) != 0 {
		t.Errorf("hits = %+v; want none for an all-stopword query", got[0].Hits)
	}
}

// DAL rejects nil queries, empty query strings, and negative limit before touching storage.
func TestSearch_ValidationErrors(t *testing.T) {
	t.Parallel()
	store, sqlDB, teardown := openConcreteStore(t)
	defer teardown()
	seedClient(t, sqlDB, "ns-a", db.DefaultClient)
	sess := seedSession(t, sqlDB, "ns-a", db.DefaultClient, 60)

	if _, err := store.Sources().Search(context.Background(), "ns-a", db.SearchInput{
		SessionID: sess.ID, Queries: nil,
	}); !errors.Is(err, db.ErrQueryRequired) {
		t.Errorf("empty queries err = %v; want ErrQueryRequired", err)
	}
	// An empty query string is rejected at the DAL boundary, not silently
	// relied on HTTP validation to catch.
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
