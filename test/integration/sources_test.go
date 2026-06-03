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

func TestSourcesUpsert_ReportsCreatedThenUpdated(t *testing.T) {
	store, sqlDB, teardown := openConcreteStore(t)
	defer teardown()

	seedClient(t, sqlDB, "ns-a", db.DefaultClient)
	sess := seedSession(t, sqlDB, "ns-a", db.DefaultClient, 60)
	ctx := context.Background()

	id, created, err := store.Sources().Upsert(ctx, "ns-a", sess.ID, "s")
	if err != nil {
		t.Fatalf("first Upsert: %v", err)
	}
	if !created {
		t.Error("first upsert: created = false; want true")
	}
	id2, created, err := store.Sources().Upsert(ctx, "ns-a", sess.ID, "s")
	if err != nil {
		t.Fatalf("re-Upsert: %v", err)
	}
	if created {
		t.Error("re-upsert: created = true; want false")
	}
	if id != id2 {
		t.Errorf("re-upsert returned id %d; want stable %d", id2, id)
	}
}

func TestSourcesUpsert_UnknownSessionMapsToNotFound(t *testing.T) {
	store, _, teardown := openConcreteStore(t)
	defer teardown()

	_, _, err := store.Sources().Upsert(context.Background(), "ns-a", 999999, "s")
	if !errors.Is(err, db.ErrSessionNotFound) {
		t.Fatalf("err = %v; want ErrSessionNotFound (no matching session row)", err)
	}
}

func TestSourcesUpsert_CrossNamespaceMapsToNotFound(t *testing.T) {
	store, sqlDB, teardown := openConcreteStore(t)
	defer teardown()

	seedClient(t, sqlDB, "ns-a", db.DefaultClient)
	sess := seedSession(t, sqlDB, "ns-a", db.DefaultClient, 60)

	_, _, err := store.Sources().Upsert(context.Background(), "ns-b", sess.ID, "s")
	if !errors.Is(err, db.ErrSessionNotFound) {
		t.Fatalf("err = %v; want ErrSessionNotFound", err)
	}
}

func TestChunks_InsertAndDeleteForSource(t *testing.T) {
	store, sqlDB, teardown := openConcreteStore(t)
	defer teardown()

	seedClient(t, sqlDB, "ns-a", db.DefaultClient)
	sess := seedSession(t, sqlDB, "ns-a", db.DefaultClient, 60)
	ctx := context.Background()

	id, _, err := store.Sources().Upsert(ctx, "ns-a", sess.ID, "s")
	if err != nil {
		t.Fatalf("Upsert: %v", err)
	}

	if err := store.Chunks().Insert(ctx, id, []db.Chunk{
		{Title: "a", Content: "alpha", ContentType: "prose"},
		{Title: "b", Content: "beta", ContentType: "prose"},
	}); err != nil {
		t.Fatalf("Insert: %v", err)
	}
	if n := countRows(t, sqlDB, `SELECT COUNT(*) FROM chunks WHERE source_id = $1`, id); n != 2 {
		t.Errorf("chunks = %d; want 2", n)
	}

	// Insert with an empty slice is a no-op.
	if err := store.Chunks().Insert(ctx, id, nil); err != nil {
		t.Fatalf("Insert (empty): %v", err)
	}
	if n := countRows(t, sqlDB, `SELECT COUNT(*) FROM chunks WHERE source_id = $1`, id); n != 2 {
		t.Errorf("chunks = %d after empty insert; want 2 unchanged", n)
	}

	// DeleteForSource clears the source's chunks.
	if err := store.Chunks().DeleteForSource(ctx, id); err != nil {
		t.Fatalf("DeleteForSource: %v", err)
	}
	if n := countRows(t, sqlDB, `SELECT COUNT(*) FROM chunks WHERE source_id = $1`, id); n != 0 {
		t.Errorf("chunks = %d; want 0 after DeleteForSource", n)
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

	seedIndexedSource(t, store, "ns-a", sess.ID, "first", []db.Chunk{{Title: "a", Content: "x", ContentType: "prose"}})
	seedIndexedSource(t, store, "ns-a", sess.ID, "second", []db.Chunk{
		{Title: "a", Content: "x", ContentType: "prose"},
		{Title: "b", Content: "y", ContentType: "prose"},
	})
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

	seedIndexedSource(t, store, "ns-a", sessA.ID, "in-a", []db.Chunk{{Title: "t", Content: "c", ContentType: "prose"}})

	got, err := store.Sources().List(context.Background(), "ns-a", sessB.ID)
	if err != nil {
		t.Fatalf("List B: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("session B sees %+v; want none (session isolation)", got)
	}
}
