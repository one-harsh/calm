// Copyright 2026 The CALM Authors
// SPDX-License-Identifier: Apache-2.0

package integration

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/one-harsh/calm/internal/db"
)

// ---------- RegisterClient ----------

func TestRegisterClient_NewRowInserted(t *testing.T) {
	store, sqlDB, teardown := openConcreteStore(t)
	defer teardown()

	if _, err := store.Clients().Register(context.Background(), "ns-a", "alice"); err != nil {
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
		if _, err := store.Clients().Register(context.Background(), "ns-a", "alice"); err != nil {
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

	if _, err := store.Clients().Register(context.Background(), "ns-a", "alice"); err != nil {
		t.Fatalf("RegisterClient ns-a: %v", err)
	}
	if _, err := store.Clients().Register(context.Background(), "ns-b", "alice"); err != nil {
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

	_, err := store.Clients().Register(context.Background(), "", "alice")
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

	_, err := store.Clients().Register(context.Background(), "ns-a", "")
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

	_, err := store.Clients().List(context.Background(), "")
	if !errors.Is(err, db.ErrNamespaceRequired) {
		t.Fatalf("want ErrNamespaceRequired, got %v", err)
	}
}

func TestListClients_NoClients(t *testing.T) {
	store, _, teardown := openConcreteStore(t)
	defer teardown()

	got, err := store.Clients().List(context.Background(), "ns-empty")
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

	got, err := store.Clients().List(context.Background(), "ns-a")
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
	seedSessionWithActivity(t, sqlDB, "ns-a", "alice", 60, newest.Add(-2*time.Hour))
	seedSessionWithActivity(t, sqlDB, "ns-a", "alice", 60, newest.Add(-time.Hour))
	seedSessionWithActivity(t, sqlDB, "ns-a", "alice", 60, newest)

	got, err := store.Clients().List(context.Background(), "ns-a")
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

	got, err := store.Clients().List(context.Background(), "ns-a")
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
	seedSession(t, sqlDB, "ns-a", "alice", 60)
	seedClient(t, sqlDB, "ns-b", "alice")
	// no sessions in ns-b

	gotB, err := store.Clients().List(context.Background(), "ns-b")
	if err != nil {
		t.Fatalf("ListClients ns-b: %v", err)
	}
	if len(gotB) != 1 || gotB[0].Name != "alice" || gotB[0].SessionCount != 0 || gotB[0].LastActivity != nil {
		t.Errorf("ns-b alice should be sessionless; got %+v", gotB)
	}

	gotA, err := store.Clients().List(context.Background(), "ns-a")
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

	n, err := store.Clients().CountSessions(context.Background(), "ns-a", "alice")
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
		seedSession(t, sqlDB, "ns-a", "alice", 60)
	}

	n, err := store.Clients().CountSessions(context.Background(), "ns-a", "alice")
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

	n, err := store.Clients().CountSessions(context.Background(), "ns-a", "ghost")
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

	_, err := store.Clients().CountSessions(context.Background(), "ns-b", "alice")
	if !errors.Is(err, db.ErrClientNotFound) {
		t.Fatalf("ns-b should not see ns-a's alice; got %v", err)
	}
}

func TestCountClientSessions_EmptyNamespace(t *testing.T) {
	store, _, teardown := openConcreteStore(t)
	defer teardown()

	_, err := store.Clients().CountSessions(context.Background(), "", "alice")
	if !errors.Is(err, db.ErrNamespaceRequired) {
		t.Fatalf("want ErrNamespaceRequired, got %v", err)
	}
}

func TestCountClientSessions_EmptyName(t *testing.T) {
	store, _, teardown := openConcreteStore(t)
	defer teardown()

	_, err := store.Clients().CountSessions(context.Background(), "ns-a", "")
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

	_, err := store.Clients().Delete(context.Background(), "ns-a", db.DefaultClient)
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

	res, err := store.Clients().Delete(context.Background(), "ns-a", "ghost")
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
	sessionIDs := make([]int64, 0, 2)
	for i := 0; i < 2; i++ {
		s := seedSession(t, sqlDB, "ns-a", "alice", 60)
		sessionIDs = append(sessionIDs, s.ID)
		seedSessionLabel(t, sqlDB, s.ID, "env", "prod")
		seedSessionLabel(t, sqlDB, s.ID, "team", "ml")
		for _, src := range []string{"src-a", "src-b"} {
			sourceID := seedSource(t, sqlDB, s.ID, src)
			for j := 0; j < 3; j++ {
				seedChunk(t, sqlDB, sourceID, "title", "content", "prose")
			}
		}
		for j := 0; j < 4; j++ {
			seedEvent(t, sqlDB, s.ID, "tool_invocation"+string(rune('0'+j)), 3, []byte(`{}`))
		}
		seedVocab(t, sqlDB, s.ID, "alpha", 1)
		seedVocab(t, sqlDB, s.ID, "beta", 2)
	}

	res, err := store.Clients().Delete(context.Background(), "ns-a", "alice")
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

	// Verify cascade actually fired across every dependent table. The id-list
	// is inlined as a literal because pgx stdlib doesn't auto-bind []int64 to
	// an array placeholder and these ids are server-minted, not workload input.
	idList := fmt.Sprintf("(%d,%d)", sessionIDs[0], sessionIDs[1])
	tables := []struct {
		name  string
		query string
	}{
		{"clients", `SELECT COUNT(*) FROM clients WHERE namespace = 'ns-a' AND name = 'alice'`},
		{"sessions", `SELECT COUNT(*) FROM sessions WHERE namespace = 'ns-a' AND client = 'alice'`},
		{"session_labels", `SELECT COUNT(*) FROM session_labels WHERE session_id IN ` + idList},
		{"sources", `SELECT COUNT(*) FROM sources WHERE session_id IN ` + idList},
		{"chunks", `SELECT COUNT(*) FROM chunks WHERE source_id IN (SELECT id FROM sources WHERE session_id IN ` + idList + `)`},
		{"session_events", `SELECT COUNT(*) FROM session_events WHERE session_id IN ` + idList},
		{"vocabulary", `SELECT COUNT(*) FROM vocabulary WHERE session_id IN ` + idList},
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
	seedSession(t, sqlDB, "ns-a", "alice", 60)
	bob := seedSession(t, sqlDB, "ns-a", "bob", 60)
	bobSource := seedSource(t, sqlDB, bob.ID, "bob-src")
	seedChunk(t, sqlDB, bobSource, "bob", "content", "prose")

	if _, err := store.Clients().Delete(context.Background(), "ns-a", "alice"); err != nil {
		t.Fatalf("DeleteClient alice: %v", err)
	}

	// bob untouched.
	if n := countRows(t, sqlDB, `SELECT COUNT(*) FROM clients WHERE namespace = 'ns-a' AND name = 'bob'`); n != 1 {
		t.Errorf("bob client row: want 1, got %d", n)
	}
	if n := countRows(t, sqlDB, `SELECT COUNT(*) FROM sessions WHERE id = $1`, bob.ID); n != 1 {
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
	seedSession(t, sqlDB, "ns-a", "alice", 60)
	seedClient(t, sqlDB, "ns-b", "alice")
	bSess := seedSession(t, sqlDB, "ns-b", "alice", 60)

	if _, err := store.Clients().Delete(context.Background(), "ns-a", "alice"); err != nil {
		t.Fatalf("DeleteClient ns-a alice: %v", err)
	}

	// ns-b alice intact.
	if n := countRows(t, sqlDB, `SELECT COUNT(*) FROM clients WHERE namespace = 'ns-b' AND name = 'alice'`); n != 1 {
		t.Errorf("ns-b alice row: want 1, got %d", n)
	}
	if n := countRows(t, sqlDB, `SELECT COUNT(*) FROM sessions WHERE id = $1`, bSess.ID); n != 1 {
		t.Errorf("ns-b session: want 1, got %d", n)
	}
}

// Regression: deleting a client in one namespace must not touch a
// same-named client (with its own sessions and dependents) in another
// namespace. Session ids are now globally-unique surrogates so the original
// "shared session_id" collision is unrepresentable; the namespace-isolation
// invariant the test guards remains worth pinning.
func TestDeleteClient_CascadeCountsScopedByNamespace(t *testing.T) {
	store, sqlDB, teardown := openConcreteStore(t)
	defer teardown()

	seedClient(t, sqlDB, "ns-a", "alice")
	seedClient(t, sqlDB, "ns-b", "alice")
	sessA := seedSession(t, sqlDB, "ns-a", "alice", 60)
	sessB := seedSession(t, sqlDB, "ns-b", "alice", 60)

	// ns-a alice: small footprint.
	seedSessionLabel(t, sqlDB, sessA.ID, "side", "a")
	srcA := seedSource(t, sqlDB, sessA.ID, "src")
	seedChunk(t, sqlDB, srcA, "t", "c", "prose")
	seedEvent(t, sqlDB, sessA.ID, "evt", 3, []byte(`{}`))

	// ns-b alice: larger footprint.
	for _, k := range []string{"side", "env", "team", "region", "tier"} {
		seedSessionLabel(t, sqlDB, sessB.ID, k, "v")
	}
	for i := 0; i < 5; i++ {
		srcB := seedSource(t, sqlDB, sessB.ID, fmt.Sprintf("src-%d", i))
		for j := 0; j < 5; j++ {
			seedChunk(t, sqlDB, srcB, "t", "c", "prose")
		}
	}
	for i := 0; i < 5; i++ {
		seedEvent(t, sqlDB, sessB.ID, fmt.Sprintf("evt-%d", i), 3, []byte(`{}`))
	}

	res, err := store.Clients().Delete(context.Background(), "ns-a", "alice")
	if err != nil {
		t.Fatalf("DeleteClient ns-a alice: %v", err)
	}
	want := db.DeleteClientResult{
		Client:          "alice",
		DeletedSessions: 1,
		Cascaded:        db.CascadeCounts{Sources: 1, Chunks: 1, Events: 1, Labels: 1},
	}
	if res != want {
		t.Errorf("ns-a cascade counts leaked from ns-b: got %+v; want %+v", res, want)
	}

	if n := countRows(t, sqlDB, `SELECT COUNT(*) FROM sessions WHERE id = $1`, sessB.ID); n != 1 {
		t.Errorf("ns-b session lost: want 1, got %d", n)
	}
	if n := countRows(t, sqlDB, `SELECT COUNT(*) FROM sources WHERE session_id = $1`, sessB.ID); n != 5 {
		t.Errorf("ns-b sources lost: want 5, got %d", n)
	}
	if n := countRows(t, sqlDB, `SELECT COUNT(*) FROM session_events WHERE session_id = $1`, sessB.ID); n != 5 {
		t.Errorf("ns-b events lost: want 5, got %d", n)
	}
	if n := countRows(t, sqlDB, `SELECT COUNT(*) FROM session_labels WHERE session_id = $1`, sessB.ID); n != 5 {
		t.Errorf("ns-b labels lost: want 5, got %d", n)
	}
}

func TestDeleteClient_NoSessionsCleanDelete(t *testing.T) {
	store, sqlDB, teardown := openConcreteStore(t)
	defer teardown()

	seedClient(t, sqlDB, "ns-a", "alice")

	res, err := store.Clients().Delete(context.Background(), "ns-a", "alice")
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

	_, err := store.Clients().Delete(context.Background(), "", "alice")
	if !errors.Is(err, db.ErrNamespaceRequired) {
		t.Fatalf("want ErrNamespaceRequired, got %v", err)
	}
}

func TestDeleteClient_EmptyName(t *testing.T) {
	store, _, teardown := openConcreteStore(t)
	defer teardown()

	_, err := store.Clients().Delete(context.Background(), "ns-a", "")
	if !errors.Is(err, db.ErrClientNameRequired) {
		t.Fatalf("want ErrClientNameRequired, got %v", err)
	}
}
