// Copyright 2026 The CALM Authors
// SPDX-License-Identifier: Apache-2.0

package integration

import (
	"context"
	"testing"
	"time"

	"github.com/one-harsh/calm/internal/db"
)

// A session with no events returns an empty snapshot without error.
func TestSnapshot_EmptySessionReturnsEmpty(t *testing.T) {
	t.Parallel()
	store, sqlDB, teardown := openConcreteStore(t)
	defer teardown()

	seedClient(t, sqlDB, "ns-a", db.DefaultClient)
	sess := seedSession(t, sqlDB, "ns-a", db.DefaultClient, 60)

	got, err := store.Events().Snapshot(context.Background(), "ns-a", sess.ID)
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("got %d events; want 0", len(got))
	}
}

// Snapshot returns events ordered by ascending priority then descending recency, so lower
// priority numbers and more-recent timestamps appear first.
func TestSnapshot_OrdersByPriorityThenRecency(t *testing.T) {
	t.Parallel()
	store, sqlDB, teardown := openConcreteStore(t)
	defer teardown()

	seedClient(t, sqlDB, "ns-a", db.DefaultClient)
	sess := seedSession(t, sqlDB, "ns-a", db.DefaultClient, 60)

	t0 := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	t1 := t0.Add(time.Minute)
	seedEventAt(t, sqlDB, sess.ID, "p2", 2, []byte(`{}`), t0)
	seedEventAt(t, sqlDB, sess.ID, "p1-old", 1, []byte(`{}`), t0)
	seedEventAt(t, sqlDB, sess.ID, "p1-new", 1, []byte(`{}`), t1)
	seedEventAt(t, sqlDB, sess.ID, "p3", 3, []byte(`{}`), t0)

	got, err := store.Events().Snapshot(context.Background(), "ns-a", sess.ID)
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	wantOrder := []string{"p1-new", "p1-old", "p2", "p3"}
	if len(got) != len(wantOrder) {
		t.Fatalf("got %d events; want %d", len(got), len(wantOrder))
	}
	for i, want := range wantOrder {
		if got[i].Type != want {
			t.Errorf("event[%d].Type = %q; want %q", i, got[i].Type, want)
		}
	}
}

// Fetching a snapshot with a session id that belongs to a different namespace returns empty;
// no error is raised (namespace-isolation: cross-namespace data is invisible).
func TestSnapshot_CrossNamespaceIsInvisible(t *testing.T) {
	t.Parallel()
	store, sqlDB, teardown := openConcreteStore(t)
	defer teardown()

	seedClient(t, sqlDB, "ns-a", db.DefaultClient)
	sess := seedSession(t, sqlDB, "ns-a", db.DefaultClient, 60)
	seedEvent(t, sqlDB, sess.ID, "leak", 1, []byte(`{}`))

	got, err := store.Events().Snapshot(context.Background(), "ns-b", sess.ID)
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("got %d events; want 0 (invisibility)", len(got))
	}
}

// When a session has more events than the DAL snapshot row cap (512), the fetch returns
// exactly the cap count rather than all rows.
func TestSnapshot_RowCapBoundsFetch(t *testing.T) {
	t.Parallel()
	store, sqlDB, teardown := openConcreteStore(t)
	defer teardown()

	seedClient(t, sqlDB, "ns-a", db.DefaultClient)
	sess := seedSession(t, sqlDB, "ns-a", db.DefaultClient, 60)

	// Bulk-seed past the DAL's snapshot row cap (512). With FIFO eviction
	// deferred, the cap is the only bound on the snapshot fetch.
	if _, err := sqlDB.ExecContext(
		context.Background(),
		`INSERT INTO session_events (session_id, type, priority, data, data_hash)
		 SELECT $1, 'bulk', 1, '{}'::jsonb, decode(md5(g::text), 'hex')
		 FROM generate_series(1, 600) g`,
		sess.ID,
	); err != nil {
		t.Fatalf("bulk seed events: %v", err)
	}

	got, err := store.Events().Snapshot(context.Background(), "ns-a", sess.ID)
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	if len(got) != 512 {
		t.Errorf("got %d events; want 512 (snapshotRowCap)", len(got))
	}
}

// Events written to session A are invisible when fetching a snapshot for session B in the
// same namespace (session-isolation: each session sees only its own events).
func TestSnapshot_SessionIsolationWithinNamespace(t *testing.T) {
	t.Parallel()
	store, sqlDB, teardown := openConcreteStore(t)
	defer teardown()

	seedClient(t, sqlDB, "ns-a", db.DefaultClient)
	sessA := seedSession(t, sqlDB, "ns-a", db.DefaultClient, 60)
	sessB := seedSession(t, sqlDB, "ns-a", db.DefaultClient, 60)

	t0 := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	seedEventAt(t, sqlDB, sessA.ID, "in-a", 1, []byte(`{}`), t0)
	seedEventAt(t, sqlDB, sessB.ID, "in-b", 1, []byte(`{}`), t0)

	got, err := store.Events().Snapshot(context.Background(), "ns-a", sessA.ID)
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	if len(got) != 1 || got[0].Type != "in-a" {
		t.Errorf("got %+v; want exactly [in-a]", got)
	}
}
