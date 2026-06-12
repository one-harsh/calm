// Copyright 2026 The CALM Authors
// SPDX-License-Identifier: Apache-2.0

package integration

import (
	"context"
	"database/sql"
	"errors"
	"testing"

	logging "github.com/one-harsh/context-logging"

	"github.com/one-harsh/calm/internal/db"
	"github.com/one-harsh/calm/internal/ingest"
)

// vocabDocFreq reads a word's doc_freq; a pruned/never-written row reads as
// absent (use countVocabRows for absence assertions).
func vocabDocFreq(t *testing.T, sqlDB *sql.DB, sessionID int64, word string) int {
	t.Helper()
	var n int
	err := sqlDB.QueryRowContext(context.Background(),
		`SELECT doc_freq FROM vocabulary WHERE session_id = $1 AND word = $2`, sessionID, word).Scan(&n)
	if errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("vocabDocFreq(%q): no vocabulary row", word)
	}
	if err != nil {
		t.Fatalf("vocabDocFreq(%q): %v", word, err)
	}
	return n
}

func countVocabRows(t *testing.T, sqlDB *sql.DB, sessionID int64, word string) int {
	t.Helper()
	return countRows(t, sqlDB, `SELECT COUNT(*) FROM vocabulary WHERE session_id = $1 AND word = $2`, sessionID, word)
}

// Indexing prose chunks populates doc_freq as chunks-containing-word: lexemes are
// deduped within a chunk (two surface forms of one stem count once), counted across
// chunks, and derived from content only — title words contribute nothing.
func TestVocabulary_IndexPopulatesDocFreq(t *testing.T) {
	t.Parallel()
	store, sqlDB, teardown := openConcreteStore(t)
	defer teardown()

	seedClient(t, sqlDB, "ns-a", db.DefaultClient)
	sess := seedSession(t, sqlDB, "ns-a", db.DefaultClient, 60)

	seedIndexedSource(t, store, "ns-a", sess.ID, "s", []db.Chunk{
		{Title: "tonly", Content: "caching cached widget", ContentType: "prose"},
		{Title: "tonly", Content: "widget gizmo", ContentType: "prose"},
	})

	if got := vocabDocFreq(t, sqlDB, sess.ID, "widget"); got != 2 {
		t.Errorf("doc_freq(widget) = %d; want 2 (one per chunk)", got)
	}
	if got := vocabDocFreq(t, sqlDB, sess.ID, "gizmo"); got != 1 {
		t.Errorf("doc_freq(gizmo) = %d; want 1", got)
	}
	if got := vocabDocFreq(t, sqlDB, sess.ID, "cach"); got != 1 {
		t.Errorf("doc_freq(cach) = %d; want 1 (caching+cached dedupe to one lexeme within the chunk)", got)
	}
	if n := countVocabRows(t, sqlDB, sess.ID, "tonly"); n != 0 {
		t.Errorf("title word indexed (rows = %d); want 0 — vocabulary derives from content only", n)
	}
}

// Code chunks tokenize with the identifier-preserving config: identifiers stay
// whole (lowercased, unsplit) and no stemming applies — matching what the code
// bm25 index makes queryable.
func TestVocabulary_CodeChunksKeepIdentifiers(t *testing.T) {
	t.Parallel()
	store, sqlDB, teardown := openConcreteStore(t)
	defer teardown()

	seedClient(t, sqlDB, "ns-a", db.DefaultClient)
	sess := seedSession(t, sqlDB, "ns-a", db.DefaultClient, 60)

	seedIndexedSource(t, store, "ns-a", sess.ID, "s", []db.Chunk{
		{Title: "t", Content: "getUserById getUserById caching", ContentType: "code"},
	})

	if got := vocabDocFreq(t, sqlDB, sess.ID, "getuserbyid"); got != 1 {
		t.Errorf("doc_freq(getuserbyid) = %d; want 1 (identifier kept whole, deduped)", got)
	}
	if got := vocabDocFreq(t, sqlDB, sess.ID, "caching"); got != 1 {
		t.Errorf("doc_freq(caching) = %d; want 1 (simple config does not stem)", got)
	}
	if n := countVocabRows(t, sqlDB, sess.ID, "cach"); n != 0 {
		t.Errorf("stemmed lexeme present (rows = %d); want 0 for code content", n)
	}
}

// Re-ingesting a source through the real ingest service decrements the prior
// chunks' contributions before the new ones are added and prunes rows reaching
// zero. Drives ingest.Service (the production WithTx composition): if the chunk
// delete ever ran before the vocabulary decrement, the old terms would survive.
func TestVocabulary_ReingestDecrementsAndPrunes(t *testing.T) {
	t.Parallel()
	store, sqlDB, teardown := openConcreteStore(t)
	defer teardown()

	seedClient(t, sqlDB, "ns-a", db.DefaultClient)
	sess := seedSession(t, sqlDB, "ns-a", db.DefaultClient, 60)
	svc := ingest.New(store, logging.Nop())
	ctx := context.Background()

	if _, err := svc.Ingest(ctx, "ns-a", sess.ID, mustNewV7(t), ingest.Input{Source: "s", Content: "alpha beta"}); err != nil {
		t.Fatalf("ingest: %v", err)
	}
	if _, err := svc.Ingest(ctx, "ns-a", sess.ID, mustNewV7(t), ingest.Input{Source: "s", Content: "beta gamma"}); err != nil {
		t.Fatalf("re-ingest: %v", err)
	}

	if n := countVocabRows(t, sqlDB, sess.ID, "alpha"); n != 0 {
		t.Errorf("alpha rows = %d; want 0 (decremented to zero and pruned)", n)
	}
	if got := vocabDocFreq(t, sqlDB, sess.ID, "beta"); got != 1 {
		t.Errorf("doc_freq(beta) = %d; want 1 (decremented then re-added)", got)
	}
	if got := vocabDocFreq(t, sqlDB, sess.ID, "gamma"); got != 1 {
		t.Errorf("doc_freq(gamma) = %d; want 1", got)
	}
}

// Re-ingesting one source leaves sibling sources' vocabulary contributions
// untouched: a word shared across two sources drops only by the re-ingested
// source's share.
func TestVocabulary_SiblingSourceContributionsPreserved(t *testing.T) {
	t.Parallel()
	store, sqlDB, teardown := openConcreteStore(t)
	defer teardown()

	seedClient(t, sqlDB, "ns-a", db.DefaultClient)
	sess := seedSession(t, sqlDB, "ns-a", db.DefaultClient, 60)
	svc := ingest.New(store, logging.Nop())
	ctx := context.Background()

	if _, err := svc.Ingest(ctx, "ns-a", sess.ID, mustNewV7(t), ingest.Input{Source: "a", Content: "omega delta"}); err != nil {
		t.Fatalf("ingest a: %v", err)
	}
	if _, err := svc.Ingest(ctx, "ns-a", sess.ID, mustNewV7(t), ingest.Input{Source: "b", Content: "omega sigma"}); err != nil {
		t.Fatalf("ingest b: %v", err)
	}
	if got := vocabDocFreq(t, sqlDB, sess.ID, "omega"); got != 2 {
		t.Fatalf("doc_freq(omega) = %d; want 2 before re-ingest", got)
	}

	if _, err := svc.Ingest(ctx, "ns-a", sess.ID, mustNewV7(t), ingest.Input{Source: "a", Content: "delta"}); err != nil {
		t.Fatalf("re-ingest a: %v", err)
	}

	if got := vocabDocFreq(t, sqlDB, sess.ID, "omega"); got != 1 {
		t.Errorf("doc_freq(omega) = %d; want 1 (b's contribution intact)", got)
	}
	if got := vocabDocFreq(t, sqlDB, sess.ID, "delta"); got != 1 {
		t.Errorf("doc_freq(delta) = %d; want 1", got)
	}
	if got := vocabDocFreq(t, sqlDB, sess.ID, "sigma"); got != 1 {
		t.Errorf("doc_freq(sigma) = %d; want 1", got)
	}
}

// Re-ingesting a source with empty content (the delete-only path) clears the
// source's unique terms from the vocabulary and only those — shared words keep
// the siblings' contributions.
func TestVocabulary_EmptyReingestClearsSourceTerms(t *testing.T) {
	t.Parallel()
	store, sqlDB, teardown := openConcreteStore(t)
	defer teardown()

	seedClient(t, sqlDB, "ns-a", db.DefaultClient)
	sess := seedSession(t, sqlDB, "ns-a", db.DefaultClient, 60)
	svc := ingest.New(store, logging.Nop())
	ctx := context.Background()

	if _, err := svc.Ingest(ctx, "ns-a", sess.ID, mustNewV7(t), ingest.Input{Source: "a", Content: "alpha omega"}); err != nil {
		t.Fatalf("ingest a: %v", err)
	}
	if _, err := svc.Ingest(ctx, "ns-a", sess.ID, mustNewV7(t), ingest.Input{Source: "b", Content: "omega"}); err != nil {
		t.Fatalf("ingest b: %v", err)
	}

	if _, err := svc.Ingest(ctx, "ns-a", sess.ID, mustNewV7(t), ingest.Input{Source: "a", Content: ""}); err != nil {
		t.Fatalf("empty re-ingest a: %v", err)
	}

	if n := countVocabRows(t, sqlDB, sess.ID, "alpha"); n != 0 {
		t.Errorf("alpha rows = %d; want 0 (a's unique term cleared)", n)
	}
	if got := vocabDocFreq(t, sqlDB, sess.ID, "omega"); got != 1 {
		t.Errorf("doc_freq(omega) = %d; want 1 (b's contribution intact)", got)
	}
}
