// Copyright 2026 The CALM Authors
// SPDX-License-Identifier: Apache-2.0

package integration

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/one-harsh/calm/internal/db"
)

func TestSourcesIndex_PersistsSourceAndChunks(t *testing.T) {
	store, sqlDB, teardown := openConcreteStore(t)
	defer teardown()

	seedClient(t, sqlDB, "ns-a", db.DefaultClient)
	sess := seedSession(t, sqlDB, "ns-a", db.DefaultClient, 60)

	err := store.Sources().Index(context.Background(), "ns-a", db.IndexInput{
		SessionID: sess.ID, Source: "build.log",
		Chunks: []db.Chunk{
			{Title: "a", Content: "alpha", ContentType: "prose"},
			{Title: "b", Content: "beta", ContentType: "prose"},
		},
	})
	if err != nil {
		t.Fatalf("Index: %v", err)
	}

	if n := countRows(t, sqlDB, `SELECT COUNT(*) FROM sources WHERE session_id = $1`, sess.ID); n != 1 {
		t.Errorf("sources = %d; want 1", n)
	}
	if n := countRows(t, sqlDB,
		`SELECT COUNT(*) FROM chunks c JOIN sources s ON c.source_id = s.id WHERE s.session_id = $1`, sess.ID); n != 2 {
		t.Errorf("chunks = %d; want 2", n)
	}
}

func TestSourcesIndex_IdempotentReingestReplaces(t *testing.T) {
	store, sqlDB, teardown := openConcreteStore(t)
	defer teardown()

	seedClient(t, sqlDB, "ns-a", db.DefaultClient)
	sess := seedSession(t, sqlDB, "ns-a", db.DefaultClient, 60)
	ctx := context.Background()

	mustIndex := func(chunks []db.Chunk) {
		if err := store.Sources().Index(ctx, "ns-a", db.IndexInput{SessionID: sess.ID, Source: "s", Chunks: chunks}); err != nil {
			t.Fatalf("Index: %v", err)
		}
	}
	mustIndex([]db.Chunk{
		{Title: "old1", Content: "x", ContentType: "prose"},
		{Title: "old2", Content: "y", ContentType: "prose"},
	})
	mustIndex([]db.Chunk{{Title: "new1", Content: "z", ContentType: "prose"}})

	if n := countRows(t, sqlDB, `SELECT COUNT(*) FROM sources WHERE session_id = $1`, sess.ID); n != 1 {
		t.Errorf("sources = %d; want 1 (re-ingest reuses the row)", n)
	}
	if n := countRows(t, sqlDB,
		`SELECT COUNT(*) FROM chunks c JOIN sources s ON c.source_id = s.id WHERE s.session_id = $1`, sess.ID); n != 1 {
		t.Errorf("chunks = %d; want 1 (old chunks replaced)", n)
	}
	if n := countRows(t, sqlDB,
		`SELECT COUNT(*) FROM chunks c JOIN sources s ON c.source_id = s.id WHERE s.session_id = $1 AND c.title = 'new1'`, sess.ID); n != 1 {
		t.Error("want the new chunk present after re-ingest")
	}
}

func TestSourcesIndex_EmptyChunksClearsContent(t *testing.T) {
	store, sqlDB, teardown := openConcreteStore(t)
	defer teardown()

	seedClient(t, sqlDB, "ns-a", db.DefaultClient)
	sess := seedSession(t, sqlDB, "ns-a", db.DefaultClient, 60)
	ctx := context.Background()

	if err := store.Sources().Index(ctx, "ns-a", db.IndexInput{
		SessionID: sess.ID, Source: "s",
		Chunks: []db.Chunk{{Title: "a", Content: "x", ContentType: "prose"}},
	}); err != nil {
		t.Fatalf("Index: %v", err)
	}
	// Re-ingest with no chunks clears content but keeps the source row.
	if err := store.Sources().Index(ctx, "ns-a", db.IndexInput{SessionID: sess.ID, Source: "s", Chunks: nil}); err != nil {
		t.Fatalf("Index empty: %v", err)
	}

	if n := countRows(t, sqlDB, `SELECT COUNT(*) FROM sources WHERE session_id = $1`, sess.ID); n != 1 {
		t.Errorf("sources = %d; want 1 (source row preserved)", n)
	}
	if n := countRows(t, sqlDB,
		`SELECT COUNT(*) FROM chunks c JOIN sources s ON c.source_id = s.id WHERE s.session_id = $1`, sess.ID); n != 0 {
		t.Errorf("chunks = %d; want 0 after empty re-ingest", n)
	}
}

func TestSourcesIndex_CrossNamespaceMapsToNotFound(t *testing.T) {
	store, sqlDB, teardown := openConcreteStore(t)
	defer teardown()

	seedClient(t, sqlDB, "ns-a", db.DefaultClient)
	sess := seedSession(t, sqlDB, "ns-a", db.DefaultClient, 60)

	err := store.Sources().Index(context.Background(), "ns-b", db.IndexInput{
		SessionID: sess.ID, Source: "s",
		Chunks: []db.Chunk{{Title: "t", Content: "c", ContentType: "prose"}},
	})
	if !errors.Is(err, db.ErrSessionNotFound) {
		t.Fatalf("err = %v; want ErrSessionNotFound", err)
	}
}

func TestSourcesList_OrdersByIndexedAtDescWithChunkCounts(t *testing.T) {
	store, sqlDB, teardown := openConcreteStore(t)
	defer teardown()

	seedClient(t, sqlDB, "ns-a", db.DefaultClient)
	sess := seedSession(t, sqlDB, "ns-a", db.DefaultClient, 60)
	ctx := context.Background()

	got, err := store.Sources().List(ctx, "ns-a", sess.ID)
	if err != nil {
		t.Fatalf("List (empty): %v", err)
	}
	if len(got) != 0 {
		t.Errorf("empty session List = %+v; want none", got)
	}

	if err := store.Sources().Index(ctx, "ns-a", db.IndexInput{
		SessionID: sess.ID, Source: "first",
		Chunks: []db.Chunk{{Title: "a", Content: "x", ContentType: "prose"}},
	}); err != nil {
		t.Fatalf("Index first: %v", err)
	}
	if err := store.Sources().Index(ctx, "ns-a", db.IndexInput{
		SessionID: sess.ID, Source: "second",
		Chunks: []db.Chunk{{Title: "a", Content: "x", ContentType: "prose"}, {Title: "b", Content: "y", ContentType: "prose"}},
	}); err != nil {
		t.Fatalf("Index second: %v", err)
	}
	// Pin indexed_at so the DESC ordering assertion is deterministic.
	t0 := time.Date(2026, 1, 1, 9, 0, 0, 0, time.UTC)
	if _, err := sqlDB.ExecContext(ctx, `UPDATE sources SET indexed_at = $2 WHERE session_id = $1 AND label = 'first'`, sess.ID, t0); err != nil {
		t.Fatalf("pin first indexed_at: %v", err)
	}
	if _, err := sqlDB.ExecContext(ctx, `UPDATE sources SET indexed_at = $2 WHERE session_id = $1 AND label = 'second'`, sess.ID, t0.Add(time.Minute)); err != nil {
		t.Fatalf("pin second indexed_at: %v", err)
	}

	got, err = store.Sources().List(ctx, "ns-a", sess.ID)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("List = %d sources; want 2", len(got))
	}
	if got[0].Label != "second" || got[0].Chunks != 2 {
		t.Errorf("got[0] = %+v; want second with 2 chunks", got[0])
	}
	if got[1].Label != "first" || got[1].Chunks != 1 {
		t.Errorf("got[1] = %+v; want first with 1 chunk", got[1])
	}
}

func TestSourcesList_SessionIsolationWithinNamespace(t *testing.T) {
	store, sqlDB, teardown := openConcreteStore(t)
	defer teardown()

	seedClient(t, sqlDB, "ns-a", db.DefaultClient)
	sessA := seedSession(t, sqlDB, "ns-a", db.DefaultClient, 60)
	sessB := seedSession(t, sqlDB, "ns-a", db.DefaultClient, 60)
	ctx := context.Background()

	if err := store.Sources().Index(ctx, "ns-a", db.IndexInput{
		SessionID: sessA.ID, Source: "in-a",
		Chunks: []db.Chunk{{Title: "t", Content: "c", ContentType: "prose"}},
	}); err != nil {
		t.Fatalf("Index: %v", err)
	}

	got, err := store.Sources().List(ctx, "ns-a", sessB.ID)
	if err != nil {
		t.Fatalf("List B: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("session B sees %+v; want none (session isolation)", got)
	}
}
