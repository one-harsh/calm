// Copyright 2026 The CALM Authors
// SPDX-License-Identifier: Apache-2.0

package integration

import (
	"context"
	"errors"
	"fmt"
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

// Code-class matching is whole-identifier at layer 1: "handleLinkerError" retrieves the
// code chunk at the primary layer (the code tokenizer keeps identifiers intact). The
// partial identifier "LinkerError" misses layer 1 and falls through to the trigram
// substring fallback.
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
	if len(got[0].Hits) != 1 || got[0].Hits[0].MatchLayer != "primary" {
		t.Fatalf("whole-identifier hits = %+v; want one primary hit", got[0].Hits)
	}
	if !strings.Contains(got[0].Hits[0].Snippet, "handleLinkerError") {
		t.Errorf("snippet = %q; want the identifier verbatim", got[0].Hits[0].Snippet)
	}
	if len(got[1].Hits) != 1 || got[1].Hits[0].MatchLayer != "trigram" {
		t.Fatalf("partial-identifier hits = %+v; want one trigram hit", got[1].Hits)
	}
	if !strings.Contains(got[1].Hits[0].Snippet, "LinkerError") {
		t.Errorf("trigram snippet = %q; want the substring verbatim", got[1].Hits[0].Snippet)
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

// A partial identifier ("ProcessRequest") catches a longer identifier ("handleProcessRequest")
// via the trigram word-similarity fallback when the code tokenizer keeps the longer form
// whole. The returned hit carries match_layer=trigram with the matched identifier verbatim
// in the snippet.
func TestSearch_TrigramFallbackOnPartialIdentifier(t *testing.T) {
	t.Parallel()
	store, sqlDB, teardown := openConcreteStore(t)
	defer teardown()
	seedClient(t, sqlDB, "ns-a", db.DefaultClient)
	sess := seedSession(t, sqlDB, "ns-a", db.DefaultClient, 60)
	seedIndexedSource(t, store, "ns-a", sess.ID, "main.go", []db.Chunk{
		{Title: "request handler", Content: "func handleProcessRequest() error { return nil }", ContentType: "code"},
	})

	got, err := store.Sources().Search(context.Background(), "ns-a", db.SearchInput{
		SessionID: sess.ID, Queries: []string{"ProcessRequest"},
	})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(got[0].Hits) != 1 || got[0].Hits[0].MatchLayer != "trigram" {
		t.Fatalf("hits = %+v; want one trigram hit", got[0].Hits)
	}
	if !strings.Contains(got[0].Hits[0].Snippet, "handleProcessRequest") {
		t.Errorf("snippet = %q; want the matched identifier verbatim", got[0].Hits[0].Snippet)
	}
}

// Multi-term query at layer 2 uses per-term AND semantics: every term must word-similar-
// match title or content. "ProcessRequest CommandHandler" qualifies only a chunk where
// both partial identifiers have similar neighbors; chunks missing either term's match are
// excluded. The code tokenizer keeps the full camelCase tokens whole so layer 1's
// stemmed lexemes don't match the partial-identifier query.
func TestSearch_TrigramFallbackPerTermAND(t *testing.T) {
	t.Parallel()
	store, sqlDB, teardown := openConcreteStore(t)
	defer teardown()
	seedClient(t, sqlDB, "ns-a", db.DefaultClient)
	sess := seedSession(t, sqlDB, "ns-a", db.DefaultClient, 60)
	seedIndexedSource(t, store, "ns-a", sess.ID, "main.go", []db.Chunk{
		// Both partial identifiers have whole-form matches.
		{Title: "both", Content: "handleProcessRequest and runCommandHandler share state", ContentType: "code"},
		// Only the first partial identifier matches.
		{Title: "process only", Content: "handleProcessRequest drives ops", ContentType: "code"},
		// Only the second partial identifier matches.
		{Title: "command only", Content: "runCommandHandler drives ops", ContentType: "code"},
	})

	got, err := store.Sources().Search(context.Background(), "ns-a", db.SearchInput{
		SessionID: sess.ID, Queries: []string{"ProcessRequest CommandHandler"},
	})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	titles := map[string]string{}
	for _, h := range got[0].Hits {
		titles[h.Title] = h.MatchLayer
	}
	if titles["both"] != "trigram" {
		t.Errorf("expected the chunk with both terms at trigram; hits = %+v", got[0].Hits)
	}
	if _, present := titles["process only"]; present {
		t.Errorf("process-only chunk should be excluded by per-term AND; hits = %+v", got[0].Hits)
	}
	if _, present := titles["command only"]; present {
		t.Errorf("command-only chunk should be excluded by per-term AND; hits = %+v", got[0].Hits)
	}
}

// Title-side similarity is weighted 2.0 vs content's 1.0 at layer 2 (mirrors layer 1):
// a chunk with the partial identifier only in title outranks a chunk with it only in
// content. The content-match chunk is seeded first (lower id) so the tie-break can't
// carry the ordering.
func TestSearch_TrigramFallbackPrioritizesTitleViaWeightedSimilarity(t *testing.T) {
	t.Parallel()
	store, sqlDB, teardown := openConcreteStore(t)
	defer teardown()
	seedClient(t, sqlDB, "ns-a", db.DefaultClient)
	sess := seedSession(t, sqlDB, "ns-a", db.DefaultClient, 60)
	seedIndexedSource(t, store, "ns-a", sess.ID, "main.go", []db.Chunk{
		{Title: "helper", Content: "wrap handleProcessRequest in retry", ContentType: "code"},
		{Title: "handleProcessRequest helper", Content: "retry on failure", ContentType: "code"},
	})

	got, err := store.Sources().Search(context.Background(), "ns-a", db.SearchInput{
		SessionID: sess.ID, Queries: []string{"ProcessRequest"},
	})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(got[0].Hits) != 2 {
		t.Fatalf("hits = %+v; want both chunks", got[0].Hits)
	}
	if got[0].Hits[0].Title != "handleProcessRequest helper" {
		t.Errorf("first hit = %q; want the title-match ranked above the content-match",
			got[0].Hits[0].Title)
	}
}

// Layer 1 finds two BM25 hits; layer 2 finds three partial-identifier chunks. With limit=3
// the combined result is exactly three (two primary, one trigram). With limit=5 it's all
// five. Layer 2 fills under the same per-query cap layer 1 was bounded by.
func TestSearch_TrigramFallbackRespectsCombinedLimit(t *testing.T) {
	t.Parallel()
	store, sqlDB, teardown := openConcreteStore(t)
	defer teardown()
	seedClient(t, sqlDB, "ns-a", db.DefaultClient)
	sess := seedSession(t, sqlDB, "ns-a", db.DefaultClient, 60)
	seedIndexedSource(t, store, "ns-a", sess.ID, "mixed.md", []db.Chunk{
		// Layer-1 BM25 matches: prose chunks store "connectionPool" as a lexeme.
		{Title: "primary 1", Content: "the connectionPool was exhausted", ContentType: "prose"},
		{Title: "primary 2", Content: "the connectionPool drained slowly", ContentType: "prose"},
		// Layer-2 only: code tokenizer keeps the camelCase identifier whole, so
		// the layer-1 lexeme is "openconnectionpool" etc., not "connectionpool".
		{Title: "fallback 1", Content: "openConnectionPool wraps the dial", ContentType: "code"},
		{Title: "fallback 2", Content: "closeConnectionPool exits cleanly", ContentType: "code"},
		{Title: "fallback 3", Content: "resetConnectionPool resets state", ContentType: "code"},
	})

	gotCapped, err := store.Sources().Search(context.Background(), "ns-a", db.SearchInput{
		SessionID: sess.ID, Queries: []string{"connectionPool"}, Limit: 3,
	})
	if err != nil {
		t.Fatalf("Search limit=3: %v", err)
	}
	if len(gotCapped[0].Hits) != 3 {
		t.Fatalf("limit=3 hits = %+v; want exactly three combined", gotCapped[0].Hits)
	}
	primaryCount, trigramCount := 0, 0
	for _, h := range gotCapped[0].Hits {
		switch h.MatchLayer {
		case "primary":
			primaryCount++
		case "trigram":
			trigramCount++
		}
	}
	if primaryCount != 2 || trigramCount != 1 {
		t.Errorf("limit=3 layer split = primary:%d trigram:%d; want 2/1", primaryCount, trigramCount)
	}

	gotFull, err := store.Sources().Search(context.Background(), "ns-a", db.SearchInput{
		SessionID: sess.ID, Queries: []string{"connectionPool"}, Limit: 5,
	})
	if err != nil {
		t.Fatalf("Search limit=5: %v", err)
	}
	if len(gotFull[0].Hits) != 5 {
		t.Errorf("limit=5 hits = %+v; want all five combined", gotFull[0].Hits)
	}
}

// A chunk that matches both layers (BM25-tokenized AND word-similar) appears once,
// tagged primary — never duplicated as a trigram hit. Layer 2 dedupes against the layer-1
// chunk ids before emitting.
func TestSearch_TrigramFallbackDeduplicatesAgainstLayer1(t *testing.T) {
	t.Parallel()
	store, sqlDB, teardown := openConcreteStore(t)
	defer teardown()
	seedClient(t, sqlDB, "ns-a", db.DefaultClient)
	sess := seedSession(t, sqlDB, "ns-a", db.DefaultClient, 60)
	// Prose chunk: "connectionPool" stores as a whole lexeme (parser keeps camelCase),
	// so query "connectionPool" matches at layer 1; the same query also word-similar-
	// matches at layer 2. Without dedup it would appear twice.
	seedIndexedSource(t, store, "ns-a", sess.ID, "notes.md", []db.Chunk{
		{Title: "pool", Content: "the connectionPool helper handles retries", ContentType: "prose"},
	})

	got, err := store.Sources().Search(context.Background(), "ns-a", db.SearchInput{
		SessionID: sess.ID, Queries: []string{"connectionPool"},
	})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(got[0].Hits) != 1 {
		t.Fatalf("hits = %+v; want one hit (no dedup leak)", got[0].Hits)
	}
	if got[0].Hits[0].MatchLayer != "primary" {
		t.Errorf("match_layer = %q; want primary (layer 1 wins the tag)", got[0].Hits[0].MatchLayer)
	}
}

// When layer 1 returns the full requested limit, layer 2 does not fire — no trigram path
// runs, no extra hits emerge from partial-identifier matches that would otherwise qualify.
func TestSearch_TrigramFallbackOnlyWhenLayer1Underfills(t *testing.T) {
	t.Parallel()
	store, sqlDB, teardown := openConcreteStore(t)
	defer teardown()
	seedClient(t, sqlDB, "ns-a", db.DefaultClient)
	sess := seedSession(t, sqlDB, "ns-a", db.DefaultClient, 60)
	seedIndexedSource(t, store, "ns-a", sess.ID, "notes.md", []db.Chunk{
		// Two prose chunks whose lexemes match the query at layer 1.
		{Title: "warn", Content: "the connectionPool was exhausted", ContentType: "prose"},
		{Title: "error", Content: "the connectionPool drained slowly", ContentType: "prose"},
		// This chunk would word-similar-match at layer 2, but layer 1 already filled the limit.
		{Title: "extra", Content: "openConnectionPool wraps the dial", ContentType: "code"},
	})

	got, err := store.Sources().Search(context.Background(), "ns-a", db.SearchInput{
		SessionID: sess.ID, Queries: []string{"connectionPool"}, Limit: 2,
	})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(got[0].Hits) != 2 {
		t.Fatalf("hits = %+v; want exactly two (no fallback)", got[0].Hits)
	}
	for _, h := range got[0].Hits {
		if h.MatchLayer != "primary" {
			t.Errorf("hit %q match_layer = %q; want primary only (layer 2 should not fire)",
				h.Title, h.MatchLayer)
		}
	}
}

// Trigram-matchable content in ns-a is invisible to a search in ns-b — the namespace
// EXISTS guard holds at layer 2 just as it does at layer 1.
func TestSearch_TrigramFallbackCrossNamespaceIsolated(t *testing.T) {
	t.Parallel()
	store, sqlDB, teardown := openConcreteStore(t)
	defer teardown()
	seedClient(t, sqlDB, "ns-a", db.DefaultClient)
	seedClient(t, sqlDB, "ns-b", db.DefaultClient)
	sessA := seedSession(t, sqlDB, "ns-a", db.DefaultClient, 60)
	sessB := seedSession(t, sqlDB, "ns-b", db.DefaultClient, 60)
	seedIndexedSource(t, store, "ns-a", sessA.ID, "main.go", []db.Chunk{
		{Title: "request handler", Content: "func handleProcessRequest() error { return nil }", ContentType: "code"},
	})

	got, err := store.Sources().Search(context.Background(), "ns-b", db.SearchInput{
		SessionID: sessB.ID, Queries: []string{"ProcessRequest"},
	})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(got[0].Hits) != 0 {
		t.Errorf("hits = %+v; want zero (cross-namespace invisibility)", got[0].Hits)
	}
}

// A long, natural-language query with stopwords and short artifacts gets normalized at
// layer 2: stopwords and sub-3-char tokens are stripped before the per-term `<<%` AND filter
// fires. Without normalization the stopwords would each require a word-similar match and
// the chunk would be excluded; here, only "ProcessRequest" survives and the hit comes back.
func TestSearch_TrigramFallbackNormalizesNoiseTerms(t *testing.T) {
	t.Parallel()
	store, sqlDB, teardown := openConcreteStore(t)
	defer teardown()
	seedClient(t, sqlDB, "ns-a", db.DefaultClient)
	sess := seedSession(t, sqlDB, "ns-a", db.DefaultClient, 60)
	seedIndexedSource(t, store, "ns-a", sess.ID, "main.go", []db.Chunk{
		{Title: "request handler", Content: "func handleProcessRequest() error { return nil }", ContentType: "code"},
	})

	got, err := store.Sources().Search(context.Background(), "ns-a", db.SearchInput{
		SessionID: sess.ID, Queries: []string{"where did the a b ProcessRequest in at it"},
	})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(got[0].Hits) != 1 || got[0].Hits[0].MatchLayer != "trigram" {
		t.Fatalf("hits = %+v; want one trigram hit (stopwords + short tokens normalized out)", got[0].Hits)
	}
}

// A pathological query with hundreds of tokens does not blow up the SQL builder, the bind-
// param list, or the planner: normalization caps the per-term AND clauses at a small N
// before the statement is built. The exact hit count isn't asserted (filler terms gate it
// out); the load-bearing assertion is that the call returns without error and within the
// integration-test deadline.
func TestSearch_TrigramFallbackBoundsExcessiveTermCount(t *testing.T) {
	t.Parallel()
	store, sqlDB, teardown := openConcreteStore(t)
	defer teardown()
	seedClient(t, sqlDB, "ns-a", db.DefaultClient)
	sess := seedSession(t, sqlDB, "ns-a", db.DefaultClient, 60)
	seedIndexedSource(t, store, "ns-a", sess.ID, "main.go", []db.Chunk{
		{Title: "request handler", Content: "func handleProcessRequest() error { return nil }", ContentType: "code"},
	})

	parts := make([]string, 0, 200)
	parts = append(parts, "ProcessRequest")
	for i := 0; i < 199; i++ {
		parts = append(parts, fmt.Sprintf("fillerterm%d", i))
	}
	query := strings.Join(parts, " ")

	if _, err := store.Sources().Search(context.Background(), "ns-a", db.SearchInput{
		SessionID: sess.ID, Queries: []string{query},
	}); err != nil {
		t.Fatalf("excessive-term query returned error: %v", err)
	}
}

// Trigram-matchable content in session A is invisible to a search in session B within the
// same namespace — session isolation holds at layer 2.
func TestSearch_TrigramFallbackSessionIsolated(t *testing.T) {
	t.Parallel()
	store, sqlDB, teardown := openConcreteStore(t)
	defer teardown()
	seedClient(t, sqlDB, "ns-a", db.DefaultClient)
	sessA := seedSession(t, sqlDB, "ns-a", db.DefaultClient, 60)
	sessB := seedSession(t, sqlDB, "ns-a", db.DefaultClient, 60)
	seedIndexedSource(t, store, "ns-a", sessA.ID, "main.go", []db.Chunk{
		{Title: "request handler", Content: "func handleProcessRequest() error { return nil }", ContentType: "code"},
	})

	got, err := store.Sources().Search(context.Background(), "ns-a", db.SearchInput{
		SessionID: sessB.ID, Queries: []string{"ProcessRequest"},
	})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(got[0].Hits) != 0 {
		t.Errorf("hits = %+v; want zero (cross-session invisibility within namespace)", got[0].Hits)
	}
}
