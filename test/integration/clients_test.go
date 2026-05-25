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

// ---------- RegisterClient ----------

func TestRegisterClient_NewRowInserted(t *testing.T) {
	store, sqlDB, teardown := openConcreteStore(t)
	defer teardown()

	if err := store.RegisterClient(context.Background(), "ns-a", "alice"); err != nil {
		t.Fatalf("RegisterClient: %v", err)
	}
	got := countRows(t, sqlDB, `SELECT COUNT(*) FROM clients WHERE namespace = $1 AND name = $2`, "ns-a", "alice")
	if got != 1 {
		t.Errorf("want 1 row, got %d", got)
	}
}

func TestRegisterClient_IdempotentOnRepeat(t *testing.T) {
	store, sqlDB, teardown := openConcreteStore(t)
	defer teardown()

	for i := 0; i < 3; i++ {
		if err := store.RegisterClient(context.Background(), "ns-a", "alice"); err != nil {
			t.Fatalf("RegisterClient (call %d): %v", i, err)
		}
	}
	got := countRows(t, sqlDB, `SELECT COUNT(*) FROM clients WHERE namespace = $1 AND name = $2`, "ns-a", "alice")
	if got != 1 {
		t.Errorf("want 1 row after 3 idempotent calls, got %d", got)
	}
}

func TestRegisterClient_CrossNamespaceIndependent(t *testing.T) {
	store, sqlDB, teardown := openConcreteStore(t)
	defer teardown()

	if err := store.RegisterClient(context.Background(), "ns-a", "alice"); err != nil {
		t.Fatalf("RegisterClient ns-a: %v", err)
	}
	if err := store.RegisterClient(context.Background(), "ns-b", "alice"); err != nil {
		t.Fatalf("RegisterClient ns-b: %v", err)
	}
	total := countRows(t, sqlDB, `SELECT COUNT(*) FROM clients WHERE name = $1`, "alice")
	if total != 2 {
		t.Errorf("want 2 rows for same name across namespaces, got %d", total)
	}
}

func TestRegisterClient_EmptyNamespace(t *testing.T) {
	store, sqlDB, teardown := openConcreteStore(t)
	defer teardown()

	err := store.RegisterClient(context.Background(), "", "alice")
	if !errors.Is(err, db.ErrNamespaceRequired) {
		t.Fatalf("want ErrNamespaceRequired, got %v", err)
	}
	if got := countRows(t, sqlDB, `SELECT COUNT(*) FROM clients`); got != 0 {
		t.Errorf("validation should not insert; got %d rows", got)
	}
}

func TestRegisterClient_EmptyName(t *testing.T) {
	store, sqlDB, teardown := openConcreteStore(t)
	defer teardown()

	err := store.RegisterClient(context.Background(), "ns-a", "")
	if !errors.Is(err, db.ErrClientNameRequired) {
		t.Fatalf("want ErrClientNameRequired, got %v", err)
	}
	if got := countRows(t, sqlDB, `SELECT COUNT(*) FROM clients`); got != 0 {
		t.Errorf("validation should not insert; got %d rows", got)
	}
}

// ---------- ListClients ----------

func TestListClients_EmptyNamespaceArg(t *testing.T) {
	store, _, teardown := openConcreteStore(t)
	defer teardown()

	_, err := store.ListClients(context.Background(), "")
	if !errors.Is(err, db.ErrNamespaceRequired) {
		t.Fatalf("want ErrNamespaceRequired, got %v", err)
	}
}

func TestListClients_NoClients(t *testing.T) {
	store, _, teardown := openConcreteStore(t)
	defer teardown()

	got, err := store.ListClients(context.Background(), "ns-empty")
	if err != nil {
		t.Fatalf("ListClients: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("want empty slice, got %d entries", len(got))
	}
	if got == nil {
		t.Error("want empty non-nil slice, got nil")
	}
}

func TestListClients_ClientNoSessions(t *testing.T) {
	store, sqlDB, teardown := openConcreteStore(t)
	defer teardown()

	seedClient(t, sqlDB, "ns-a", "alice")

	got, err := store.ListClients(context.Background(), "ns-a")
	if err != nil {
		t.Fatalf("ListClients: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("want 1 client, got %d", len(got))
	}
	if got[0].Name != "alice" || got[0].SessionCount != 0 {
		t.Errorf("got %+v; want {alice, 0, nil}", got[0])
	}
	if got[0].LastActivity != nil {
		t.Errorf("LastActivity should be nil for no-sessions client; got %v", *got[0].LastActivity)
	}
}

func TestListClients_ClientWithSessions(t *testing.T) {
	store, sqlDB, teardown := openConcreteStore(t)
	defer teardown()

	seedClient(t, sqlDB, "ns-a", "alice")
	newest := time.Now().UTC().Truncate(time.Microsecond)
	seedSessionWithActivity(t, sqlDB, "ns-a", "alice", "s1", 60, newest.Add(-2*time.Hour))
	seedSessionWithActivity(t, sqlDB, "ns-a", "alice", "s2", 60, newest.Add(-time.Hour))
	seedSessionWithActivity(t, sqlDB, "ns-a", "alice", "s3", 60, newest)

	got, err := store.ListClients(context.Background(), "ns-a")
	if err != nil {
		t.Fatalf("ListClients: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("want 1 client, got %d", len(got))
	}
	if got[0].SessionCount != 3 {
		t.Errorf("SessionCount: want 3, got %d", got[0].SessionCount)
	}
	if got[0].LastActivity == nil {
		t.Fatal("LastActivity nil; want max session time")
	}
	if diff := got[0].LastActivity.Sub(newest); diff > time.Millisecond || diff < -time.Millisecond {
		t.Errorf("LastActivity %v; want ~%v (diff %v)", *got[0].LastActivity, newest, diff)
	}
}

func TestListClients_MultipleClientsSortedByName(t *testing.T) {
	store, sqlDB, teardown := openConcreteStore(t)
	defer teardown()

	// Insert in deliberately unsorted order.
	seedClient(t, sqlDB, "ns-a", "charlie")
	seedClient(t, sqlDB, "ns-a", "alice")
	seedClient(t, sqlDB, "ns-a", "bob")

	got, err := store.ListClients(context.Background(), "ns-a")
	if err != nil {
		t.Fatalf("ListClients: %v", err)
	}
	wantOrder := []string{"alice", "bob", "charlie"}
	if len(got) != len(wantOrder) {
		t.Fatalf("want %d clients, got %d", len(wantOrder), len(got))
	}
	for i, want := range wantOrder {
		if got[i].Name != want {
			t.Errorf("position %d: got %q, want %q", i, got[i].Name, want)
		}
	}
}

func TestListClients_CrossNamespaceIsolation(t *testing.T) {
	store, sqlDB, teardown := openConcreteStore(t)
	defer teardown()

	seedClient(t, sqlDB, "ns-a", "alice")
	seedSession(t, sqlDB, "ns-a", "alice", "s1", 60)
	seedClient(t, sqlDB, "ns-b", "alice")
	// no sessions in ns-b

	gotB, err := store.ListClients(context.Background(), "ns-b")
	if err != nil {
		t.Fatalf("ListClients ns-b: %v", err)
	}
	if len(gotB) != 1 || gotB[0].Name != "alice" || gotB[0].SessionCount != 0 || gotB[0].LastActivity != nil {
		t.Errorf("ns-b alice should be sessionless; got %+v", gotB)
	}

	gotA, err := store.ListClients(context.Background(), "ns-a")
	if err != nil {
		t.Fatalf("ListClients ns-a: %v", err)
	}
	if len(gotA) != 1 || gotA[0].SessionCount != 1 {
		t.Errorf("ns-a alice should have 1 session; got %+v", gotA)
	}
}

// ---------- CountClientSessions ----------

func TestCountClientSessions_Zero(t *testing.T) {
	store, sqlDB, teardown := openConcreteStore(t)
	defer teardown()

	seedClient(t, sqlDB, "ns-a", "alice")

	n, err := store.CountClientSessions(context.Background(), "ns-a", "alice")
	if err != nil {
		t.Fatalf("CountClientSessions: %v", err)
	}
	if n != 0 {
		t.Errorf("want 0 sessions, got %d", n)
	}
}

func TestCountClientSessions_Multiple(t *testing.T) {
	store, sqlDB, teardown := openConcreteStore(t)
	defer teardown()

	seedClient(t, sqlDB, "ns-a", "alice")
	for i := 0; i < 5; i++ {
		seedSession(t, sqlDB, "ns-a", "alice", randHex(8), 60)
	}

	n, err := store.CountClientSessions(context.Background(), "ns-a", "alice")
	if err != nil {
		t.Fatalf("CountClientSessions: %v", err)
	}
	if n != 5 {
		t.Errorf("want 5 sessions, got %d", n)
	}
}

func TestCountClientSessions_UnknownClient(t *testing.T) {
	store, _, teardown := openConcreteStore(t)
	defer teardown()

	n, err := store.CountClientSessions(context.Background(), "ns-a", "ghost")
	if !errors.Is(err, db.ErrClientNotFound) {
		t.Fatalf("want ErrClientNotFound, got %v", err)
	}
	if n != 0 {
		t.Errorf("unknown client should return 0; got %d", n)
	}
}

func TestCountClientSessions_CrossNamespaceUnknown(t *testing.T) {
	store, sqlDB, teardown := openConcreteStore(t)
	defer teardown()

	seedClient(t, sqlDB, "ns-a", "alice")

	_, err := store.CountClientSessions(context.Background(), "ns-b", "alice")
	if !errors.Is(err, db.ErrClientNotFound) {
		t.Fatalf("ns-b should not see ns-a's alice; got %v", err)
	}
}

func TestCountClientSessions_EmptyNamespace(t *testing.T) {
	store, _, teardown := openConcreteStore(t)
	defer teardown()

	_, err := store.CountClientSessions(context.Background(), "", "alice")
	if !errors.Is(err, db.ErrNamespaceRequired) {
		t.Fatalf("want ErrNamespaceRequired, got %v", err)
	}
}

func TestCountClientSessions_EmptyName(t *testing.T) {
	store, _, teardown := openConcreteStore(t)
	defer teardown()

	_, err := store.CountClientSessions(context.Background(), "ns-a", "")
	if !errors.Is(err, db.ErrClientNameRequired) {
		t.Fatalf("want ErrClientNameRequired, got %v", err)
	}
}

// ---------- DeleteClient ----------

func TestDeleteClient_DefaultProtected(t *testing.T) {
	store, sqlDB, teardown := openConcreteStore(t)
	defer teardown()

	seedClient(t, sqlDB, "ns-a", db.DefaultClient)
	before := countRows(t, sqlDB, `SELECT COUNT(*) FROM clients`)

	_, err := store.DeleteClient(context.Background(), "ns-a", db.DefaultClient)
	if !errors.Is(err, db.ErrClientProtected) {
		t.Fatalf("want ErrClientProtected, got %v", err)
	}
	if got := countRows(t, sqlDB, `SELECT COUNT(*) FROM clients`); got != before {
		t.Errorf("row count changed; want %d, got %d", before, got)
	}
}

func TestDeleteClient_UnknownClient(t *testing.T) {
	store, _, teardown := openConcreteStore(t)
	defer teardown()

	res, err := store.DeleteClient(context.Background(), "ns-a", "ghost")
	if !errors.Is(err, db.ErrClientNotFound) {
		t.Fatalf("want ErrClientNotFound, got %v", err)
	}
	if res != (db.DeleteClientResult{}) {
		t.Errorf("want zero-valued result; got %+v", res)
	}
}

func TestDeleteClient_HappyPathFullCascade(t *testing.T) {
	store, sqlDB, teardown := openConcreteStore(t)
	defer teardown()

	seedClient(t, sqlDB, "ns-a", "alice")

	// Two sessions, each with 2 labels, 2 sources × 3 chunks, 4 events, vocab entries.
	for _, sid := range []string{"s1", "s2"} {
		seedSession(t, sqlDB, "ns-a", "alice", sid, 60)
		seedSessionLabel(t, sqlDB, sid, "env", "prod")
		seedSessionLabel(t, sqlDB, sid, "team", "ml")
		for _, src := range []string{"src-a", "src-b"} {
			sourceID := seedSource(t, sqlDB, sid, src)
			for j := 0; j < 3; j++ {
				seedChunk(t, sqlDB, sourceID, "title", "content", "prose")
			}
		}
		for j := 0; j < 4; j++ {
			seedEvent(t, sqlDB, sid, "tool_invocation"+string(rune('0'+j)), 3, []byte(`{}`))
		}
		seedVocab(t, sqlDB, sid, "alpha", 1)
		seedVocab(t, sqlDB, sid, "beta", 2)
	}

	res, err := store.DeleteClient(context.Background(), "ns-a", "alice")
	if err != nil {
		t.Fatalf("DeleteClient: %v", err)
	}

	want := db.DeleteClientResult{
		Client:          "alice",
		DeletedSessions: 2,
		Cascaded:        db.CascadeCounts{Sources: 4, Chunks: 12, Events: 8, Labels: 4},
	}
	if res != want {
		t.Errorf("result: got %+v; want %+v", res, want)
	}

	// Verify cascade actually fired across every dependent table.
	tables := []struct {
		name  string
		query string
	}{
		{"clients", `SELECT COUNT(*) FROM clients WHERE namespace = 'ns-a' AND name = 'alice'`},
		{"sessions", `SELECT COUNT(*) FROM sessions WHERE namespace = 'ns-a' AND client = 'alice'`},
		{"session_labels", `SELECT COUNT(*) FROM session_labels WHERE session_id IN ('s1','s2')`},
		{"sources", `SELECT COUNT(*) FROM sources WHERE session_id IN ('s1','s2')`},
		{"chunks", `SELECT COUNT(*) FROM chunks WHERE source_id IN (SELECT id FROM sources WHERE session_id IN ('s1','s2'))`},
		{"session_events", `SELECT COUNT(*) FROM session_events WHERE session_id IN ('s1','s2')`},
		{"vocabulary", `SELECT COUNT(*) FROM vocabulary WHERE session_id IN ('s1','s2')`},
	}
	for _, tbl := range tables {
		if n := countRows(t, sqlDB, tbl.query); n != 0 {
			t.Errorf("post-delete %s rows: want 0, got %d", tbl.name, n)
		}
	}
}

func TestDeleteClient_NegativeIsolation(t *testing.T) {
	store, sqlDB, teardown := openConcreteStore(t)
	defer teardown()

	seedClient(t, sqlDB, "ns-a", "alice")
	seedClient(t, sqlDB, "ns-a", "bob")
	seedSession(t, sqlDB, "ns-a", "alice", "alice-1", 60)
	seedSession(t, sqlDB, "ns-a", "bob", "bob-1", 60)
	bobSource := seedSource(t, sqlDB, "bob-1", "bob-src")
	seedChunk(t, sqlDB, bobSource, "bob", "content", "prose")

	if _, err := store.DeleteClient(context.Background(), "ns-a", "alice"); err != nil {
		t.Fatalf("DeleteClient alice: %v", err)
	}

	// bob untouched.
	if n := countRows(t, sqlDB, `SELECT COUNT(*) FROM clients WHERE namespace = 'ns-a' AND name = 'bob'`); n != 1 {
		t.Errorf("bob client row: want 1, got %d", n)
	}
	if n := countRows(t, sqlDB, `SELECT COUNT(*) FROM sessions WHERE session_id = 'bob-1'`); n != 1 {
		t.Errorf("bob session: want 1, got %d", n)
	}
	if n := countRows(t, sqlDB, `SELECT COUNT(*) FROM chunks WHERE source_id = $1`, bobSource); n != 1 {
		t.Errorf("bob chunks: want 1, got %d", n)
	}
}

func TestDeleteClient_CrossNamespaceIsolation(t *testing.T) {
	store, sqlDB, teardown := openConcreteStore(t)
	defer teardown()

	seedClient(t, sqlDB, "ns-a", "alice")
	seedSession(t, sqlDB, "ns-a", "alice", "a-s1", 60)
	seedClient(t, sqlDB, "ns-b", "alice")
	seedSession(t, sqlDB, "ns-b", "alice", "b-s1", 60)

	if _, err := store.DeleteClient(context.Background(), "ns-a", "alice"); err != nil {
		t.Fatalf("DeleteClient ns-a alice: %v", err)
	}

	// ns-b alice intact.
	if n := countRows(t, sqlDB, `SELECT COUNT(*) FROM clients WHERE namespace = 'ns-b' AND name = 'alice'`); n != 1 {
		t.Errorf("ns-b alice row: want 1, got %d", n)
	}
	if n := countRows(t, sqlDB, `SELECT COUNT(*) FROM sessions WHERE session_id = 'b-s1'`); n != 1 {
		t.Errorf("ns-b session: want 1, got %d", n)
	}
}

func TestDeleteClient_NoSessionsCleanDelete(t *testing.T) {
	store, sqlDB, teardown := openConcreteStore(t)
	defer teardown()

	seedClient(t, sqlDB, "ns-a", "alice")

	res, err := store.DeleteClient(context.Background(), "ns-a", "alice")
	if err != nil {
		t.Fatalf("DeleteClient: %v", err)
	}
	want := db.DeleteClientResult{Client: "alice"} // zero CascadeCounts
	if res != want {
		t.Errorf("result: got %+v; want %+v", res, want)
	}
	if n := countRows(t, sqlDB, `SELECT COUNT(*) FROM clients WHERE namespace = 'ns-a' AND name = 'alice'`); n != 0 {
		t.Errorf("client row should be gone; got %d", n)
	}
}

func TestDeleteClient_EmptyNamespace(t *testing.T) {
	store, _, teardown := openConcreteStore(t)
	defer teardown()

	_, err := store.DeleteClient(context.Background(), "", "alice")
	if !errors.Is(err, db.ErrNamespaceRequired) {
		t.Fatalf("want ErrNamespaceRequired, got %v", err)
	}
}

func TestDeleteClient_EmptyName(t *testing.T) {
	store, _, teardown := openConcreteStore(t)
	defer teardown()

	_, err := store.DeleteClient(context.Background(), "ns-a", "")
	if !errors.Is(err, db.ErrClientNameRequired) {
		t.Fatalf("want ErrClientNameRequired, got %v", err)
	}
}
