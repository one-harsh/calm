// Copyright 2026 The CALM Authors
// SPDX-License-Identifier: Apache-2.0

package integration

import (
	"context"
	"slices"
	"testing"

	"github.com/one-harsh/calm/internal/db"
)

func docChunks(titles ...string) []db.Chunk {
	out := make([]db.Chunk, len(titles))
	for i, ti := range titles {
		out[i] = db.Chunk{Title: ti, Content: "body of " + ti, ContentType: "prose"}
	}
	return out
}

func chunkTitles(chunks []db.DocChunk) []string {
	out := make([]string, len(chunks))
	for i, c := range chunks {
		out[i] = c.Title
	}
	return out
}

// Document order is insertion order and survives idempotent re-ingest: a source
// re-indexed with fresh content returns the new chunks in the order they were
// written, never interleaved with the replaced revision.
func TestChunksInOrder_StableAcrossReingest(t *testing.T) {
	t.Parallel()
	store, sqlDB, teardown := openConcreteStore(t)
	defer teardown()
	seedClient(t, sqlDB, "ns-a", db.DefaultClient)
	sess := seedSession(t, sqlDB, "ns-a", db.DefaultClient, 60)

	seedIndexedSource(t, store, "ns-a", sess.ID, "cap.log", docChunks("A", "B", "C"))
	got, hasMore, err := store.Sources().ChunksInOrder(context.Background(), "ns-a",
		db.DocOrderInput{SessionID: sess.ID, Source: "cap.log", Limit: 10, Offset: 0})
	if err != nil {
		t.Fatalf("ChunksInOrder: %v", err)
	}
	if hasMore {
		t.Error("hasMore = true; want false (all three fit)")
	}
	if titles := chunkTitles(got); !slices.Equal(titles, []string{"A", "B", "C"}) {
		t.Fatalf("order = %v; want [A B C]", titles)
	}

	seedIndexedSource(t, store, "ns-a", sess.ID, "cap.log", docChunks("X", "Y", "Z"))
	got, _, err = store.Sources().ChunksInOrder(context.Background(), "ns-a",
		db.DocOrderInput{SessionID: sess.ID, Source: "cap.log", Limit: 10, Offset: 0})
	if err != nil {
		t.Fatalf("ChunksInOrder (post-reingest): %v", err)
	}
	if titles := chunkTitles(got); !slices.Equal(titles, []string{"X", "Y", "Z"}) {
		t.Fatalf("post-reingest order = %v; want [X Y Z] (fresh content, in insertion order)", titles)
	}
}

// Successive offset windows tile the source with no gaps or overlaps, and hasMore
// stays true until the final window — the pagination contract a sequential reread
// walks.
func TestChunksInOrder_OffsetWindows(t *testing.T) {
	t.Parallel()
	store, sqlDB, teardown := openConcreteStore(t)
	defer teardown()
	seedClient(t, sqlDB, "ns-a", db.DefaultClient)
	sess := seedSession(t, sqlDB, "ns-a", db.DefaultClient, 60)
	seedIndexedSource(t, store, "ns-a", sess.ID, "cap.log", docChunks("c0", "c1", "c2", "c3", "c4"))

	var walked []string
	offset := 0
	for {
		got, hasMore, err := store.Sources().ChunksInOrder(context.Background(), "ns-a",
			db.DocOrderInput{SessionID: sess.ID, Source: "cap.log", Limit: 2, Offset: offset})
		if err != nil {
			t.Fatalf("ChunksInOrder(offset=%d): %v", offset, err)
		}
		if len(got) > 2 {
			t.Fatalf("window at offset %d returned %d chunks; want <= limit 2", offset, len(got))
		}
		walked = append(walked, chunkTitles(got)...)
		offset += len(got)
		if !hasMore {
			break
		}
		if len(got) == 0 {
			t.Fatalf("hasMore=true with an empty window at offset %d — would loop", offset)
		}
	}
	if !slices.Equal(walked, []string{"c0", "c1", "c2", "c3", "c4"}) {
		t.Fatalf("walk = %v; want all five in order with no gaps/overlaps", walked)
	}
}

// Chunks indexed in namespace A are invisible to a document-order read from
// namespace B (namespace-isolation: cross-namespace data is empty, not an error).
func TestChunksInOrder_NamespaceIsolated(t *testing.T) {
	t.Parallel()
	store, sqlDB, teardown := openConcreteStore(t)
	defer teardown()
	seedClient(t, sqlDB, "ns-a", db.DefaultClient)
	sess := seedSession(t, sqlDB, "ns-a", db.DefaultClient, 60)
	seedIndexedSource(t, store, "ns-a", sess.ID, "cap.log", docChunks("A", "B"))

	got, hasMore, err := store.Sources().ChunksInOrder(context.Background(), "ns-b",
		db.DocOrderInput{SessionID: sess.ID, Source: "cap.log", Limit: 10, Offset: 0})
	if err != nil {
		t.Fatalf("ChunksInOrder: %v", err)
	}
	if len(got) != 0 || hasMore {
		t.Errorf("cross-namespace read = %v (hasMore=%v); want empty", chunkTitles(got), hasMore)
	}
}

// Chunks indexed in session A are invisible to a document-order read scoped to
// session B in the same namespace (session-isolation: content boundary).
func TestChunksInOrder_SessionIsolated(t *testing.T) {
	t.Parallel()
	store, sqlDB, teardown := openConcreteStore(t)
	defer teardown()
	seedClient(t, sqlDB, "ns-a", db.DefaultClient)
	sessA := seedSession(t, sqlDB, "ns-a", db.DefaultClient, 60)
	sessB := seedSession(t, sqlDB, "ns-a", db.DefaultClient, 60)
	seedIndexedSource(t, store, "ns-a", sessA.ID, "cap.log", docChunks("A", "B"))

	got, _, err := store.Sources().ChunksInOrder(context.Background(), "ns-a",
		db.DocOrderInput{SessionID: sessB.ID, Source: "cap.log", Limit: 10, Offset: 0})
	if err != nil {
		t.Fatalf("ChunksInOrder: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("session B sees %v; want none (session isolation)", chunkTitles(got))
	}
}

// A nonexistent source, a zero-chunk source, and an offset past the end all return
// an empty page with no error — invisibility-consistent with source-scoped ranked
// search, not a 404.
func TestChunksInOrder_UnknownSourceEmpty(t *testing.T) {
	t.Parallel()
	store, sqlDB, teardown := openConcreteStore(t)
	defer teardown()
	seedClient(t, sqlDB, "ns-a", db.DefaultClient)
	sess := seedSession(t, sqlDB, "ns-a", db.DefaultClient, 60)

	// Nonexistent source label.
	got, hasMore, err := store.Sources().ChunksInOrder(context.Background(), "ns-a",
		db.DocOrderInput{SessionID: sess.ID, Source: "never-indexed", Limit: 10, Offset: 0})
	if err != nil || len(got) != 0 || hasMore {
		t.Errorf("unknown source: got=%v hasMore=%v err=%v; want empty/false/nil", chunkTitles(got), hasMore, err)
	}

	// Zero-chunk source (row exists, no chunks).
	seedIndexedSource(t, store, "ns-a", sess.ID, "empty.log", []db.Chunk{})
	got, hasMore, err = store.Sources().ChunksInOrder(context.Background(), "ns-a",
		db.DocOrderInput{SessionID: sess.ID, Source: "empty.log", Limit: 10, Offset: 0})
	if err != nil || len(got) != 0 || hasMore {
		t.Errorf("zero-chunk source: got=%v hasMore=%v err=%v; want empty/false/nil", chunkTitles(got), hasMore, err)
	}

	// Offset past the end of a real source.
	seedIndexedSource(t, store, "ns-a", sess.ID, "cap.log", docChunks("A", "B"))
	got, hasMore, err = store.Sources().ChunksInOrder(context.Background(), "ns-a",
		db.DocOrderInput{SessionID: sess.ID, Source: "cap.log", Limit: 10, Offset: 50})
	if err != nil || len(got) != 0 || hasMore {
		t.Errorf("offset past end: got=%v hasMore=%v err=%v; want empty/false/nil", chunkTitles(got), hasMore, err)
	}
}
