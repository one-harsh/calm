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

// A workload registers a new client; the record must appear exactly once in the DB.
func TestRegisterClient_NewRowInserted(t *testing.T) {
	t.Parallel()
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

// Registering the same client N times produces exactly one row; the idempotent-indexing guarantee applies to client registration.
func TestRegisterClient_IdempotentOnRepeat(t *testing.T) {
	t.Parallel()
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

// The same client name registered in two namespaces creates two independent rows; namespace-isolation prevents cross-namespace merging.
func TestRegisterClient_CrossNamespaceIndependent(t *testing.T) {
	t.Parallel()
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

// Empty namespace is rejected before any DB write; ErrNamespaceRequired is returned.
func TestRegisterClient_EmptyNamespace(t *testing.T) {
	t.Parallel()
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

// Empty client name is rejected before any DB write; ErrClientNameRequired is returned.
func TestRegisterClient_EmptyName(t *testing.T) {
	t.Parallel()
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

// Empty namespace argument is rejected immediately; ErrNamespaceRequired is returned.
func TestListClients_EmptyNamespaceArg(t *testing.T) {
	t.Parallel()
	store, _, teardown := openConcreteStore(t)
	defer teardown()

	_, err := store.Clients().List(context.Background(), "")
	if !errors.Is(err, db.ErrNamespaceRequired) {
		t.Fatalf("want ErrNamespaceRequired, got %v", err)
	}
}

// Listing an empty namespace returns a non-nil empty slice, not nil.
func TestListClients_NoClients(t *testing.T) {
	t.Parallel()
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

// A registered client with no sessions lists with SessionCount=0 and nil LastActivity.
func TestListClients_ClientNoSessions(t *testing.T) {
	t.Parallel()
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

// A client with multiple sessions reports the correct count and the most-recent session's activity as LastActivity.
func TestListClients_ClientWithSessions(t *testing.T) {
	t.Parallel()
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

// Clients are returned in stable alphabetical order regardless of insertion order.
func TestListClients_MultipleClientsSortedByName(t *testing.T) {
	t.Parallel()
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

// Session counts for a client in one namespace are invisible to a query against a different namespace; namespace-isolation is enforced at the list level.
func TestListClients_CrossNamespaceIsolation(t *testing.T) {
	t.Parallel()
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

// A client with no sessions reports a count of zero.
func TestCountClientSessions_Zero(t *testing.T) {
	t.Parallel()
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

// Session count reflects all active sessions belonging to the client.
func TestCountClientSessions_Multiple(t *testing.T) {
	t.Parallel()
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

// Counting sessions for a non-existent client returns ErrClientNotFound.
func TestCountClientSessions_UnknownClient(t *testing.T) {
	t.Parallel()
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

// A client registered in one namespace is invisible to a count query from another namespace; namespace-isolation applies to CountSessions.
func TestCountClientSessions_CrossNamespaceUnknown(t *testing.T) {
	t.Parallel()
	store, sqlDB, teardown := openConcreteStore(t)
	defer teardown()

	seedClient(t, sqlDB, "ns-a", "alice")

	_, err := store.Clients().CountSessions(context.Background(), "ns-b", "alice")
	if !errors.Is(err, db.ErrClientNotFound) {
		t.Fatalf("ns-b should not see ns-a's alice; got %v", err)
	}
}

// Empty namespace argument is rejected; ErrNamespaceRequired is returned.
func TestCountClientSessions_EmptyNamespace(t *testing.T) {
	t.Parallel()
	store, _, teardown := openConcreteStore(t)
	defer teardown()

	_, err := store.Clients().CountSessions(context.Background(), "", "alice")
	if !errors.Is(err, db.ErrNamespaceRequired) {
		t.Fatalf("want ErrNamespaceRequired, got %v", err)
	}
}

// Empty client name argument is rejected; ErrClientNameRequired is returned.
func TestCountClientSessions_EmptyName(t *testing.T) {
	t.Parallel()
	store, _, teardown := openConcreteStore(t)
	defer teardown()

	_, err := store.Clients().CountSessions(context.Background(), "ns-a", "")
	if !errors.Is(err, db.ErrClientNameRequired) {
		t.Fatalf("want ErrClientNameRequired, got %v", err)
	}
}

// ---------- DeleteClient ----------

// The default client cannot be deleted; ErrClientProtected is returned and no rows are removed.
func TestDeleteClient_DefaultProtected(t *testing.T) {
	t.Parallel()
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

// Deleting a non-existent client returns ErrClientNotFound and a zero-valued result.
func TestDeleteClient_UnknownClient(t *testing.T) {
	t.Parallel()
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

// Deleting a client removes the client row and all dependent sessions, sources, chunks, events, labels, and vocab in a single cascade; the result struct reports accurate counts.
func TestDeleteClient_HappyPathFullCascade(t *testing.T) {
	t.Parallel()
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

// Deleting one client in a namespace leaves other clients and their data untouched; session-isolation holds within the same namespace.
func TestDeleteClient_NegativeIsolation(t *testing.T) {
	t.Parallel()
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

// Deleting a client in namespace A leaves the same-named client and its sessions in namespace B intact; namespace-isolation is enforced during cascade.
func TestDeleteClient_CrossNamespaceIsolation(t *testing.T) {
	t.Parallel()
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

// Cascade counts returned after a delete reflect only the deleted namespace's data; the same-named client's footprint in another namespace is not included and that namespace is not touched.
//
// Regression: deleting a client in one namespace must not touch a
// same-named client (with its own sessions and dependents) in another
// namespace. Session ids are now globally-unique surrogates so the original
// "shared session_id" collision is unrepresentable; the namespace-isolation
// invariant the test guards remains worth pinning.
func TestDeleteClient_CascadeCountsScopedByNamespace(t *testing.T) {
	t.Parallel()
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

// A client with no sessions is deleted cleanly; the result reports zero cascade counts.
func TestDeleteClient_NoSessionsCleanDelete(t *testing.T) {
	t.Parallel()
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

// Empty namespace argument is rejected; ErrNamespaceRequired is returned.
func TestDeleteClient_EmptyNamespace(t *testing.T) {
	t.Parallel()
	store, _, teardown := openConcreteStore(t)
	defer teardown()

	_, err := store.Clients().Delete(context.Background(), "", "alice")
	if !errors.Is(err, db.ErrNamespaceRequired) {
		t.Fatalf("want ErrNamespaceRequired, got %v", err)
	}
}

// Empty client name argument is rejected; ErrClientNameRequired is returned.
func TestDeleteClient_EmptyName(t *testing.T) {
	t.Parallel()
	store, _, teardown := openConcreteStore(t)
	defer teardown()

	_, err := store.Clients().Delete(context.Background(), "ns-a", "")
	if !errors.Is(err, db.ErrClientNameRequired) {
		t.Fatalf("want ErrClientNameRequired, got %v", err)
	}
}
