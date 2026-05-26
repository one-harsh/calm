// Copyright 2026 The CALM Authors
// SPDX-License-Identifier: Apache-2.0

package integration

import (
	"context"
	"database/sql"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/one-harsh/calm/internal/db"
	"github.com/one-harsh/calm/internal/session"
)

// ---------- CreateSession ----------

// Repo-level Create tests pre-seed the (namespace, default-client) row because
// the repo no longer auto-registers — that orchestration lives in
// session.Service.Create. Service-level auto-register coverage is below.

func TestCreateSession_HappyMinimal(t *testing.T) {
	store, sqlDB, teardown := openConcreteStore(t)
	defer teardown()

	seedClient(t, sqlDB, "ns-a", db.DefaultClient)
	if err := store.Sessions().Create(context.Background(), &db.Session{
		ID: "s1", Namespace: "ns-a", TTLMinutes: 60,
	}); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	got := countRows(t, sqlDB,
		`SELECT COUNT(*) FROM sessions WHERE namespace = $1 AND session_id = $2`,
		"ns-a", "s1",
	)
	if got != 1 {
		t.Errorf("want 1 session row, got %d", got)
	}
}

func TestCreateSession_HappyWithLabels(t *testing.T) {
	store, sqlDB, teardown := openConcreteStore(t)
	defer teardown()

	seedClient(t, sqlDB, "ns-a", db.DefaultClient)
	labels := map[string]string{"env": "prod", "team": "ml"}
	if err := store.Sessions().Create(context.Background(), &db.Session{
		ID: "s1", Namespace: "ns-a", TTLMinutes: 60, Labels: labels,
	}); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	got := countRows(t, sqlDB,
		`SELECT COUNT(*) FROM session_labels WHERE namespace = $1 AND session_id = $2`,
		"ns-a", "s1",
	)
	if got != 2 {
		t.Errorf("want 2 label rows, got %d", got)
	}
}

// session.Service.Create is the DL01 auto-attribution boundary; this test
// covers the service-layer orchestration that the repo no longer owns.
func TestSessionService_Create_AutoRegistersNewClient(t *testing.T) {
	store, sqlDB, teardown := openConcreteStore(t)
	defer teardown()

	svc := session.New(store, 10_000)
	if err := svc.Create(context.Background(), &db.Session{
		ID: "s1", Namespace: "ns-a", Client: "alice", TTLMinutes: 60,
	}); err != nil {
		t.Fatalf("service Create: %v", err)
	}
	if n := countRows(t, sqlDB,
		`SELECT COUNT(*) FROM clients WHERE namespace = $1 AND name = $2`,
		"ns-a", "alice",
	); n != 1 {
		t.Errorf("want 1 client row, got %d", n)
	}
	if n := countRows(t, sqlDB,
		`SELECT COUNT(*) FROM sessions WHERE namespace = $1 AND session_id = $2`,
		"ns-a", "s1",
	); n != 1 {
		t.Errorf("want 1 session row, got %d", n)
	}
}

func TestSessionService_Create_DefaultsEmptyClient(t *testing.T) {
	store, sqlDB, teardown := openConcreteStore(t)
	defer teardown()

	svc := session.New(store, 10_000)
	if err := svc.Create(context.Background(), &db.Session{
		ID: "s1", Namespace: "ns-a", TTLMinutes: 60,
	}); err != nil {
		t.Fatalf("service Create: %v", err)
	}
	if n := countRows(t, sqlDB,
		`SELECT COUNT(*) FROM clients WHERE namespace = $1 AND name = $2`,
		"ns-a", db.DefaultClient,
	); n != 1 {
		t.Errorf("want default-client row, got %d", n)
	}
}

func TestCreateSession_ExistingClientIdempotent(t *testing.T) {
	store, sqlDB, teardown := openConcreteStore(t)
	defer teardown()

	seedClient(t, sqlDB, "ns-a", "alice")
	if err := store.Sessions().Create(context.Background(), &db.Session{
		ID: "s1", Namespace: "ns-a", Client: "alice", TTLMinutes: 60,
	}); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	if n := countRows(t, sqlDB,
		`SELECT COUNT(*) FROM clients WHERE namespace = $1 AND name = $2`,
		"ns-a", "alice",
	); n != 1 {
		t.Errorf("want 1 client row, got %d", n)
	}
}

func TestCreateSession_DuplicateIDSameNamespaceRejected(t *testing.T) {
	store, sqlDB, teardown := openConcreteStore(t)
	defer teardown()

	seedClient(t, sqlDB, "ns-a", db.DefaultClient)
	sess := db.Session{ID: "s1", Namespace: "ns-a", TTLMinutes: 60}
	if err := store.Sessions().Create(context.Background(), &sess); err != nil {
		t.Fatalf("first CreateSession: %v", err)
	}
	err := store.Sessions().Create(context.Background(), &sess)
	if !errors.Is(err, db.ErrSessionExists) {
		t.Errorf("second CreateSession: want ErrSessionExists, got %v", err)
	}
}

func TestCreateSession_DuplicateIDAcrossNamespacesAllowed(t *testing.T) {
	store, sqlDB, teardown := openConcreteStore(t)
	defer teardown()

	seedClient(t, sqlDB, "ns-a", db.DefaultClient)
	seedClient(t, sqlDB, "ns-b", db.DefaultClient)
	if err := store.Sessions().Create(context.Background(), &db.Session{
		ID: "s1", Namespace: "ns-a", TTLMinutes: 60,
	}); err != nil {
		t.Fatalf("CreateSession ns-a: %v", err)
	}
	if err := store.Sessions().Create(context.Background(), &db.Session{
		ID: "s1", Namespace: "ns-b", TTLMinutes: 60,
	}); err != nil {
		t.Errorf("CreateSession ns-b (same id, different namespace): want nil, got %v", err)
	}
	if n := countRows(t, sqlDB, `SELECT COUNT(*) FROM sessions WHERE session_id = 's1'`); n != 2 {
		t.Errorf("want 2 distinct sessions with id 's1', got %d", n)
	}
}

func TestCreateSession_EmptySessionID(t *testing.T) {
	store, _, teardown := openConcreteStore(t)
	defer teardown()
	err := store.Sessions().Create(context.Background(), &db.Session{Namespace: "ns-a", TTLMinutes: 60})
	if !errors.Is(err, db.ErrSessionIDRequired) {
		t.Errorf("want ErrSessionIDRequired, got %v", err)
	}
}

func TestCreateSession_EmptyNamespace(t *testing.T) {
	store, _, teardown := openConcreteStore(t)
	defer teardown()
	err := store.Sessions().Create(context.Background(), &db.Session{ID: "s1", TTLMinutes: 60})
	if !errors.Is(err, db.ErrNamespaceRequired) {
		t.Errorf("want ErrNamespaceRequired, got %v", err)
	}
}

// ---------- GetSession ----------

func TestGetSession_HappyNoLabels(t *testing.T) {
	store, sqlDB, teardown := openConcreteStore(t)
	defer teardown()

	seedClient(t, sqlDB, "ns-a", db.DefaultClient)
	seedSession(t, sqlDB, "ns-a", db.DefaultClient, "s1", 60)

	got, err := store.Sessions().Get(context.Background(), "ns-a", "s1")
	if err != nil {
		t.Fatalf("GetSession: %v", err)
	}
	if got.ID != "s1" || got.Namespace != "ns-a" || got.Client != db.DefaultClient || got.TTLMinutes != 60 {
		t.Errorf("session fields: got %+v", got)
	}
	if got.Labels != nil {
		t.Errorf("expected nil labels for label-less session; got %+v", got.Labels)
	}
}

func TestGetSession_HappyWithLabels(t *testing.T) {
	store, sqlDB, teardown := openConcreteStore(t)
	defer teardown()

	seedClient(t, sqlDB, "ns-a", db.DefaultClient)
	seedSession(t, sqlDB, "ns-a", db.DefaultClient, "s1", 60)
	seedSessionLabel(t, sqlDB, "ns-a", "s1", "env", "prod")
	seedSessionLabel(t, sqlDB, "ns-a", "s1", "team", "ml")

	got, err := store.Sessions().Get(context.Background(), "ns-a", "s1")
	if err != nil {
		t.Fatalf("GetSession: %v", err)
	}
	want := map[string]string{"env": "prod", "team": "ml"}
	if !reflect.DeepEqual(got.Labels, want) {
		t.Errorf("labels: got %+v; want %+v", got.Labels, want)
	}
}

func TestGetSession_NotFound(t *testing.T) {
	store, _, teardown := openConcreteStore(t)
	defer teardown()
	_, err := store.Sessions().Get(context.Background(), "ns-a", "missing")
	if !errors.Is(err, db.ErrSessionNotFound) {
		t.Errorf("want ErrSessionNotFound, got %v", err)
	}
}

func TestGetSession_CrossNamespaceReturnsNotFound(t *testing.T) {
	store, sqlDB, teardown := openConcreteStore(t)
	defer teardown()

	seedClient(t, sqlDB, "ns-a", db.DefaultClient)
	seedSession(t, sqlDB, "ns-a", db.DefaultClient, "s1", 60)

	_, err := store.Sessions().Get(context.Background(), "ns-b", "s1")
	if !errors.Is(err, db.ErrSessionNotFound) {
		t.Errorf("want ErrSessionNotFound (invisibility), got %v", err)
	}
}

func TestGetSession_SameIDDifferentNamespaceReturnsOwn(t *testing.T) {
	store, sqlDB, teardown := openConcreteStore(t)
	defer teardown()

	seedClient(t, sqlDB, "ns-a", db.DefaultClient)
	seedClient(t, sqlDB, "ns-b", db.DefaultClient)
	seedSession(t, sqlDB, "ns-a", db.DefaultClient, "s1", 60)
	seedSession(t, sqlDB, "ns-b", db.DefaultClient, "s1", 90)
	seedSessionLabel(t, sqlDB, "ns-a", "s1", "side", "a")
	seedSessionLabel(t, sqlDB, "ns-b", "s1", "side", "b")

	got, err := store.Sessions().Get(context.Background(), "ns-a", "s1")
	if err != nil {
		t.Fatalf("GetSession ns-a: %v", err)
	}
	if got.Namespace != "ns-a" || got.TTLMinutes != 60 || got.Labels["side"] != "a" {
		t.Errorf("ns-a session: got %+v", got)
	}
	got, err = store.Sessions().Get(context.Background(), "ns-b", "s1")
	if err != nil {
		t.Fatalf("GetSession ns-b: %v", err)
	}
	if got.Namespace != "ns-b" || got.TTLMinutes != 90 || got.Labels["side"] != "b" {
		t.Errorf("ns-b session: got %+v", got)
	}
}

func TestGetSession_EmptyNamespace(t *testing.T) {
	store, _, teardown := openConcreteStore(t)
	defer teardown()
	_, err := store.Sessions().Get(context.Background(), "", "s1")
	if !errors.Is(err, db.ErrNamespaceRequired) {
		t.Errorf("want ErrNamespaceRequired, got %v", err)
	}
}

func TestGetSession_EmptyID(t *testing.T) {
	store, _, teardown := openConcreteStore(t)
	defer teardown()
	_, err := store.Sessions().Get(context.Background(), "ns-a", "")
	if !errors.Is(err, db.ErrSessionIDRequired) {
		t.Errorf("want ErrSessionIDRequired, got %v", err)
	}
}

// ---------- TouchSession ----------

func TestTouchSession_AdvancesTimestamp(t *testing.T) {
	store, sqlDB, teardown := openConcreteStore(t)
	defer teardown()

	seedClient(t, sqlDB, "ns-a", db.DefaultClient)
	past := time.Now().Add(-1 * time.Hour).UTC()
	seedSessionWithActivity(t, sqlDB, "ns-a", db.DefaultClient, "s1", 60, past)

	future := time.Now().Add(1 * time.Hour).UTC()
	if err := store.Sessions().Touch(context.Background(), "ns-a", "s1", future); err != nil {
		t.Fatalf("TouchSession: %v", err)
	}

	var got time.Time
	if err := sqlDB.QueryRowContext(context.Background(),
		`SELECT last_activity FROM sessions WHERE namespace = $1 AND session_id = $2`,
		"ns-a", "s1",
	).Scan(&got); err != nil {
		t.Fatalf("read last_activity: %v", err)
	}
	if !got.Equal(future) && !got.After(future.Add(-1*time.Millisecond)) {
		t.Errorf("last_activity not advanced: got %v, want >= %v", got, future)
	}
}

func TestTouchSession_MonotonicIgnoresPast(t *testing.T) {
	store, sqlDB, teardown := openConcreteStore(t)
	defer teardown()

	seedClient(t, sqlDB, "ns-a", db.DefaultClient)
	now := time.Now().UTC()
	seedSessionWithActivity(t, sqlDB, "ns-a", db.DefaultClient, "s1", 60, now)

	past := now.Add(-1 * time.Hour)
	if err := store.Sessions().Touch(context.Background(), "ns-a", "s1", past); err != nil {
		t.Fatalf("TouchSession (past): %v", err)
	}
	var got time.Time
	if err := sqlDB.QueryRowContext(context.Background(),
		`SELECT last_activity FROM sessions WHERE namespace = $1 AND session_id = $2`,
		"ns-a", "s1",
	).Scan(&got); err != nil {
		t.Fatalf("read last_activity: %v", err)
	}
	if got.Before(now.Add(-1 * time.Millisecond)) {
		t.Errorf("last_activity moved backwards: got %v, want >= %v", got, now)
	}
}

func TestTouchSession_NotFound(t *testing.T) {
	store, _, teardown := openConcreteStore(t)
	defer teardown()
	err := store.Sessions().Touch(context.Background(), "ns-a", "missing", time.Now())
	if !errors.Is(err, db.ErrSessionNotFound) {
		t.Errorf("want ErrSessionNotFound, got %v", err)
	}
}

func TestTouchSession_CrossNamespaceNotFound(t *testing.T) {
	store, sqlDB, teardown := openConcreteStore(t)
	defer teardown()
	seedClient(t, sqlDB, "ns-a", db.DefaultClient)
	seedSession(t, sqlDB, "ns-a", db.DefaultClient, "s1", 60)
	err := store.Sessions().Touch(context.Background(), "ns-b", "s1", time.Now())
	if !errors.Is(err, db.ErrSessionNotFound) {
		t.Errorf("want ErrSessionNotFound, got %v", err)
	}
}

func TestTouchSession_EmptyNamespace(t *testing.T) {
	store, _, teardown := openConcreteStore(t)
	defer teardown()
	err := store.Sessions().Touch(context.Background(), "", "s1", time.Now())
	if !errors.Is(err, db.ErrNamespaceRequired) {
		t.Errorf("want ErrNamespaceRequired, got %v", err)
	}
}

func TestTouchSession_EmptyID(t *testing.T) {
	store, _, teardown := openConcreteStore(t)
	defer teardown()
	err := store.Sessions().Touch(context.Background(), "ns-a", "", time.Now())
	if !errors.Is(err, db.ErrSessionIDRequired) {
		t.Errorf("want ErrSessionIDRequired, got %v", err)
	}
}

// ---------- DeleteSession ----------

func TestDeleteSession_HappyPathFullCascade(t *testing.T) {
	store, sqlDB, teardown := openConcreteStore(t)
	defer teardown()

	seedClient(t, sqlDB, "ns-a", db.DefaultClient)
	seedSession(t, sqlDB, "ns-a", db.DefaultClient, "s1", 60)
	seedSessionLabel(t, sqlDB, "ns-a", "s1", "env", "prod")
	seedSessionLabel(t, sqlDB, "ns-a", "s1", "team", "ml")
	for _, src := range []string{"src-a", "src-b"} {
		sourceID := seedSource(t, sqlDB, "ns-a", "s1", src)
		for j := 0; j < 3; j++ {
			seedChunk(t, sqlDB, sourceID, "title", "content", "prose")
		}
	}
	for j := 0; j < 4; j++ {
		seedEvent(t, sqlDB, "ns-a", "s1", "tool_invocation"+string(rune('0'+j)), 3, []byte(`{}`))
	}
	seedVocab(t, sqlDB, "ns-a", "s1", "alpha", 1)
	seedVocab(t, sqlDB, "ns-a", "s1", "beta", 2)

	res, err := store.Sessions().Delete(context.Background(), "ns-a", "s1")
	if err != nil {
		t.Fatalf("DeleteSession: %v", err)
	}

	want := db.DeleteSessionResult{
		SessionID: "s1",
		Cascaded:  db.CascadeCounts{Sources: 2, Chunks: 6, Events: 4, Labels: 2},
	}
	if res != want {
		t.Errorf("result: got %+v; want %+v", res, want)
	}

	tables := []struct {
		name  string
		query string
	}{
		{"sessions", `SELECT COUNT(*) FROM sessions WHERE namespace = 'ns-a' AND session_id = 's1'`},
		{"session_labels", `SELECT COUNT(*) FROM session_labels WHERE namespace = 'ns-a' AND session_id = 's1'`},
		{"sources", `SELECT COUNT(*) FROM sources WHERE namespace = 'ns-a' AND session_id = 's1'`},
		{"chunks", `SELECT COUNT(*) FROM chunks WHERE source_id IN (SELECT id FROM sources WHERE namespace = 'ns-a' AND session_id = 's1')`},
		{"session_events", `SELECT COUNT(*) FROM session_events WHERE namespace = 'ns-a' AND session_id = 's1'`},
		{"vocabulary", `SELECT COUNT(*) FROM vocabulary WHERE namespace = 'ns-a' AND session_id = 's1'`},
	}
	for _, tbl := range tables {
		if n := countRows(t, sqlDB, tbl.query); n != 0 {
			t.Errorf("post-delete %s rows: want 0, got %d", tbl.name, n)
		}
	}
}

func TestDeleteSession_NotFound(t *testing.T) {
	store, _, teardown := openConcreteStore(t)
	defer teardown()
	_, err := store.Sessions().Delete(context.Background(), "ns-a", "missing")
	if !errors.Is(err, db.ErrSessionNotFound) {
		t.Errorf("want ErrSessionNotFound, got %v", err)
	}
}

func TestDeleteSession_CrossNamespaceNotFound(t *testing.T) {
	store, sqlDB, teardown := openConcreteStore(t)
	defer teardown()

	seedClient(t, sqlDB, "ns-a", db.DefaultClient)
	seedSession(t, sqlDB, "ns-a", db.DefaultClient, "s1", 60)

	_, err := store.Sessions().Delete(context.Background(), "ns-b", "s1")
	if !errors.Is(err, db.ErrSessionNotFound) {
		t.Errorf("want ErrSessionNotFound, got %v", err)
	}
	if n := countRows(t, sqlDB, `SELECT COUNT(*) FROM sessions WHERE namespace = 'ns-a' AND session_id = 's1'`); n != 1 {
		t.Errorf("ns-a row should be untouched, got count %d", n)
	}
}

func TestDeleteSession_SameIDDifferentNamespaceIsolated(t *testing.T) {
	store, sqlDB, teardown := openConcreteStore(t)
	defer teardown()

	seedClient(t, sqlDB, "ns-a", db.DefaultClient)
	seedClient(t, sqlDB, "ns-b", db.DefaultClient)
	seedSession(t, sqlDB, "ns-a", db.DefaultClient, "s1", 60)
	seedSession(t, sqlDB, "ns-b", db.DefaultClient, "s1", 60)
	seedSessionLabel(t, sqlDB, "ns-a", "s1", "side", "a")
	seedSessionLabel(t, sqlDB, "ns-b", "s1", "side", "b")
	srcB := seedSource(t, sqlDB, "ns-b", "s1", "src")
	seedChunk(t, sqlDB, srcB, "title", "content", "prose")

	if _, err := store.Sessions().Delete(context.Background(), "ns-a", "s1"); err != nil {
		t.Fatalf("DeleteSession ns-a: %v", err)
	}

	if n := countRows(t, sqlDB, `SELECT COUNT(*) FROM sessions WHERE namespace = 'ns-b' AND session_id = 's1'`); n != 1 {
		t.Errorf("ns-b session lost: want 1, got %d", n)
	}
	if n := countRows(t, sqlDB, `SELECT COUNT(*) FROM session_labels WHERE namespace = 'ns-b' AND session_id = 's1'`); n != 1 {
		t.Errorf("ns-b label lost: want 1, got %d", n)
	}
	if n := countRows(t, sqlDB, `SELECT COUNT(*) FROM sources WHERE namespace = 'ns-b' AND session_id = 's1'`); n != 1 {
		t.Errorf("ns-b source lost: want 1, got %d", n)
	}
}

func TestDeleteSession_NoChildrenCleanDelete(t *testing.T) {
	store, sqlDB, teardown := openConcreteStore(t)
	defer teardown()

	seedClient(t, sqlDB, "ns-a", db.DefaultClient)
	seedSession(t, sqlDB, "ns-a", db.DefaultClient, "s1", 60)

	res, err := store.Sessions().Delete(context.Background(), "ns-a", "s1")
	if err != nil {
		t.Fatalf("DeleteSession: %v", err)
	}
	want := db.DeleteSessionResult{SessionID: "s1"}
	if res != want {
		t.Errorf("result: got %+v; want %+v", res, want)
	}
}

func TestDeleteSession_NegativeIsolation(t *testing.T) {
	store, sqlDB, teardown := openConcreteStore(t)
	defer teardown()

	seedClient(t, sqlDB, "ns-a", db.DefaultClient)
	seedSession(t, sqlDB, "ns-a", db.DefaultClient, "sess-A", 60)
	seedSession(t, sqlDB, "ns-a", db.DefaultClient, "sess-B", 60)
	srcA := seedSource(t, sqlDB, "ns-a", "sess-A", "a")
	seedChunk(t, sqlDB, srcA, "ta", "ca", "prose")
	srcB := seedSource(t, sqlDB, "ns-a", "sess-B", "b")
	seedChunk(t, sqlDB, srcB, "tb", "cb", "prose")

	if _, err := store.Sessions().Delete(context.Background(), "ns-a", "sess-A"); err != nil {
		t.Fatalf("DeleteSession sess-A: %v", err)
	}

	if n := countRows(t, sqlDB, `SELECT COUNT(*) FROM sources WHERE namespace = 'ns-a' AND session_id = 'sess-B'`); n != 1 {
		t.Errorf("sess-B sources lost: want 1, got %d", n)
	}
	if n := countRows(t, sqlDB, `SELECT COUNT(*) FROM chunks WHERE source_id = $1`, srcB); n != 1 {
		t.Errorf("sess-B chunks lost: want 1, got %d", n)
	}
}

func TestDeleteSession_EmptyNamespace(t *testing.T) {
	store, _, teardown := openConcreteStore(t)
	defer teardown()
	_, err := store.Sessions().Delete(context.Background(), "", "s1")
	if !errors.Is(err, db.ErrNamespaceRequired) {
		t.Errorf("want ErrNamespaceRequired, got %v", err)
	}
}

func TestDeleteSession_EmptyID(t *testing.T) {
	store, _, teardown := openConcreteStore(t)
	defer teardown()
	_, err := store.Sessions().Delete(context.Background(), "ns-a", "")
	if !errors.Is(err, db.ErrSessionIDRequired) {
		t.Errorf("want ErrSessionIDRequired, got %v", err)
	}
}

// ---------- ListManagedSessions ----------

func TestListManagedSessions_EmptyNamespace(t *testing.T) {
	store, _, teardown := openConcreteStore(t)
	defer teardown()
	_, err := store.Sessions().List(context.Background(), db.ListSessionsFilter{})
	if !errors.Is(err, db.ErrNamespaceRequired) {
		t.Errorf("want ErrNamespaceRequired, got %v", err)
	}
}

func TestListManagedSessions_NoSessions(t *testing.T) {
	store, _, teardown := openConcreteStore(t)
	defer teardown()
	got, err := store.Sessions().List(context.Background(), db.ListSessionsFilter{Namespace: "ns-a"})
	if err != nil {
		t.Fatalf("ListManagedSessions: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("want empty result, got %d sessions", len(got))
	}
}

func TestListManagedSessions_SingleSessionNoLabels(t *testing.T) {
	store, sqlDB, teardown := openConcreteStore(t)
	defer teardown()

	seedClient(t, sqlDB, "ns-a", db.DefaultClient)
	seedSession(t, sqlDB, "ns-a", db.DefaultClient, "s1", 60)

	got, err := store.Sessions().List(context.Background(), db.ListSessionsFilter{Namespace: "ns-a"})
	if err != nil {
		t.Fatalf("ListManagedSessions: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("want 1 session, got %d", len(got))
	}
	if got[0].ID != "s1" || got[0].EventCount != 0 || got[0].Labels != nil {
		t.Errorf("session: got %+v", got[0])
	}
}

func TestListManagedSessions_LabelsAndEventCount(t *testing.T) {
	store, sqlDB, teardown := openConcreteStore(t)
	defer teardown()

	seedClient(t, sqlDB, "ns-a", db.DefaultClient)
	seedSession(t, sqlDB, "ns-a", db.DefaultClient, "s1", 60)
	seedSessionLabel(t, sqlDB, "ns-a", "s1", "env", "prod")
	seedSessionLabel(t, sqlDB, "ns-a", "s1", "team", "ml")
	seedSessionLabel(t, sqlDB, "ns-a", "s1", "region", "us")
	for j := 0; j < 5; j++ {
		seedEvent(t, sqlDB, "ns-a", "s1", "evt"+string(rune('0'+j)), 3, []byte(`{}`))
	}

	got, err := store.Sessions().List(context.Background(), db.ListSessionsFilter{Namespace: "ns-a"})
	if err != nil {
		t.Fatalf("ListManagedSessions: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("want 1 session, got %d", len(got))
	}
	if got[0].EventCount != 5 {
		t.Errorf("event_count: want 5, got %d", got[0].EventCount)
	}
	want := map[string]string{"env": "prod", "team": "ml", "region": "us"}
	if !reflect.DeepEqual(got[0].Labels, want) {
		t.Errorf("labels: got %+v, want %+v", got[0].Labels, want)
	}
}

func TestListManagedSessions_OrderedByLastActivityDesc(t *testing.T) {
	store, sqlDB, teardown := openConcreteStore(t)
	defer teardown()

	seedClient(t, sqlDB, "ns-a", db.DefaultClient)
	base := time.Now().UTC()
	seedSessionWithActivity(t, sqlDB, "ns-a", db.DefaultClient, "old", 60, base.Add(-2*time.Hour))
	seedSessionWithActivity(t, sqlDB, "ns-a", db.DefaultClient, "newest", 60, base)
	seedSessionWithActivity(t, sqlDB, "ns-a", db.DefaultClient, "middle", 60, base.Add(-1*time.Hour))

	got, err := store.Sessions().List(context.Background(), db.ListSessionsFilter{Namespace: "ns-a"})
	if err != nil {
		t.Fatalf("ListManagedSessions: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("want 3 sessions, got %d", len(got))
	}
	wantOrder := []string{"newest", "middle", "old"}
	for i, w := range wantOrder {
		if got[i].ID != w {
			t.Errorf("position %d: want %q, got %q", i, w, got[i].ID)
		}
	}
}

func TestListManagedSessions_ClientFilter(t *testing.T) {
	store, sqlDB, teardown := openConcreteStore(t)
	defer teardown()

	seedClient(t, sqlDB, "ns-a", "alice")
	seedClient(t, sqlDB, "ns-a", "bob")
	seedSession(t, sqlDB, "ns-a", "alice", "a1", 60)
	seedSession(t, sqlDB, "ns-a", "alice", "a2", 60)
	seedSession(t, sqlDB, "ns-a", "bob", "b1", 60)

	got, err := store.Sessions().List(context.Background(), db.ListSessionsFilter{Namespace: "ns-a", Client: "alice"})
	if err != nil {
		t.Fatalf("ListManagedSessions: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("want 2 alice sessions, got %d", len(got))
	}
	for _, s := range got {
		if s.Client != "alice" {
			t.Errorf("session %q: client got %q, want alice", s.ID, s.Client)
		}
	}
}

func TestListManagedSessions_LabelFilterSingleKey(t *testing.T) {
	store, sqlDB, teardown := openConcreteStore(t)
	defer teardown()

	seedClient(t, sqlDB, "ns-a", db.DefaultClient)
	seedSession(t, sqlDB, "ns-a", db.DefaultClient, "match", 60)
	seedSession(t, sqlDB, "ns-a", db.DefaultClient, "nomatch", 60)
	seedSessionLabel(t, sqlDB, "ns-a", "match", "env", "prod")
	seedSessionLabel(t, sqlDB, "ns-a", "nomatch", "env", "dev")

	got, err := store.Sessions().List(context.Background(), db.ListSessionsFilter{
		Namespace: "ns-a",
		Labels:    map[string]string{"env": "prod"},
	})
	if err != nil {
		t.Fatalf("ListManagedSessions: %v", err)
	}
	if len(got) != 1 || got[0].ID != "match" {
		t.Errorf("want [match], got %+v", got)
	}
}

func TestListManagedSessions_LabelFilterMultiKeyAND(t *testing.T) {
	store, sqlDB, teardown := openConcreteStore(t)
	defer teardown()

	seedClient(t, sqlDB, "ns-a", db.DefaultClient)
	seedSession(t, sqlDB, "ns-a", db.DefaultClient, "both", 60)
	seedSession(t, sqlDB, "ns-a", db.DefaultClient, "onlyenv", 60)
	seedSessionLabel(t, sqlDB, "ns-a", "both", "env", "prod")
	seedSessionLabel(t, sqlDB, "ns-a", "both", "region", "us")
	seedSessionLabel(t, sqlDB, "ns-a", "onlyenv", "env", "prod")

	got, err := store.Sessions().List(context.Background(), db.ListSessionsFilter{
		Namespace: "ns-a",
		Labels:    map[string]string{"env": "prod", "region": "us"},
	})
	if err != nil {
		t.Fatalf("ListManagedSessions: %v", err)
	}
	if len(got) != 1 || got[0].ID != "both" {
		t.Errorf("want [both] (both labels match), got %+v", got)
	}
}

func TestListManagedSessions_CrossNamespaceIsolation(t *testing.T) {
	store, sqlDB, teardown := openConcreteStore(t)
	defer teardown()

	seedClient(t, sqlDB, "ns-a", db.DefaultClient)
	seedClient(t, sqlDB, "ns-b", db.DefaultClient)
	seedSession(t, sqlDB, "ns-a", db.DefaultClient, "shared-id", 60)
	seedSession(t, sqlDB, "ns-b", db.DefaultClient, "shared-id", 60)
	seedSession(t, sqlDB, "ns-b", db.DefaultClient, "b-only", 60)

	got, err := store.Sessions().List(context.Background(), db.ListSessionsFilter{Namespace: "ns-b"})
	if err != nil {
		t.Fatalf("ListManagedSessions ns-b: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("want 2 ns-b sessions, got %d", len(got))
	}
	for _, s := range got {
		if s.Namespace != "ns-b" {
			t.Errorf("session %q: namespace got %q, want ns-b", s.ID, s.Namespace)
		}
	}
}

// ---------- CountSessions ----------

func TestCountSessions_EmptyNamespace(t *testing.T) {
	store, _, teardown := openConcreteStore(t)
	defer teardown()
	_, err := store.Sessions().Count(context.Background(), db.ListSessionsFilter{})
	if !errors.Is(err, db.ErrNamespaceRequired) {
		t.Errorf("want ErrNamespaceRequired, got %v", err)
	}
}

func TestCountSessions_Zero(t *testing.T) {
	store, _, teardown := openConcreteStore(t)
	defer teardown()
	got, err := store.Sessions().Count(context.Background(), db.ListSessionsFilter{Namespace: "ns-a"})
	if err != nil {
		t.Fatalf("CountSessions: %v", err)
	}
	if got != 0 {
		t.Errorf("want 0, got %d", got)
	}
}

func TestCountSessions_HappyWithFilters(t *testing.T) {
	store, sqlDB, teardown := openConcreteStore(t)
	defer teardown()

	seedClient(t, sqlDB, "ns-a", "alice")
	seedClient(t, sqlDB, "ns-a", "bob")
	for i := 0; i < 5; i++ {
		sid := "alice-" + string(rune('0'+i))
		seedSession(t, sqlDB, "ns-a", "alice", sid, 60)
		seedSessionLabel(t, sqlDB, "ns-a", sid, "env", "prod")
	}
	seedSession(t, sqlDB, "ns-a", "bob", "bob-1", 60)

	got, err := store.Sessions().Count(context.Background(), db.ListSessionsFilter{
		Namespace: "ns-a", Client: "alice", Labels: map[string]string{"env": "prod"},
	})
	if err != nil {
		t.Fatalf("CountSessions: %v", err)
	}
	if got != 5 {
		t.Errorf("want 5, got %d", got)
	}
}

func TestCountSessions_CrossNamespaceIsolation(t *testing.T) {
	store, sqlDB, teardown := openConcreteStore(t)
	defer teardown()

	seedClient(t, sqlDB, "ns-a", db.DefaultClient)
	seedClient(t, sqlDB, "ns-b", db.DefaultClient)
	seedSession(t, sqlDB, "ns-a", db.DefaultClient, "s1", 60)
	seedSession(t, sqlDB, "ns-a", db.DefaultClient, "s2", 60)
	seedSession(t, sqlDB, "ns-b", db.DefaultClient, "s3", 60)

	got, err := store.Sessions().Count(context.Background(), db.ListSessionsFilter{Namespace: "ns-b"})
	if err != nil {
		t.Fatalf("CountSessions: %v", err)
	}
	if got != 1 {
		t.Errorf("want 1 ns-b session, got %d", got)
	}
}

// ---------- DeleteSessions ----------

func TestDeleteSessions_EmptyNamespace(t *testing.T) {
	store, _, teardown := openConcreteStore(t)
	defer teardown()
	_, err := store.Sessions().DeleteAll(context.Background(), db.ListSessionsFilter{})
	if !errors.Is(err, db.ErrNamespaceRequired) {
		t.Errorf("want ErrNamespaceRequired, got %v", err)
	}
}

func TestDeleteSessions_NoMatch(t *testing.T) {
	store, _, teardown := openConcreteStore(t)
	defer teardown()
	res, err := store.Sessions().DeleteAll(context.Background(), db.ListSessionsFilter{Namespace: "ns-a"})
	if err != nil {
		t.Fatalf("DeleteSessions: %v", err)
	}
	if res != (db.DeleteSessionsResult{}) {
		t.Errorf("want zero result, got %+v", res)
	}
}

func TestDeleteSessions_HappyBulkCascade(t *testing.T) {
	store, sqlDB, teardown := openConcreteStore(t)
	defer teardown()

	seedClient(t, sqlDB, "ns-a", db.DefaultClient)
	for _, sid := range []string{"s1", "s2", "s3"} {
		seedSession(t, sqlDB, "ns-a", db.DefaultClient, sid, 60)
		seedSessionLabel(t, sqlDB, "ns-a", sid, "env", "prod")
		srcID := seedSource(t, sqlDB, "ns-a", sid, "src")
		seedChunk(t, sqlDB, srcID, "t", "c", "prose")
		seedChunk(t, sqlDB, srcID, "t", "c", "prose")
		seedEvent(t, sqlDB, "ns-a", sid, "evt", 3, []byte(`{}`))
	}

	res, err := store.Sessions().DeleteAll(context.Background(), db.ListSessionsFilter{Namespace: "ns-a"})
	if err != nil {
		t.Fatalf("DeleteSessions: %v", err)
	}
	want := db.DeleteSessionsResult{
		DeletedSessions: 3,
		Cascaded:        db.CascadeCounts{Sources: 3, Chunks: 6, Events: 3, Labels: 3},
	}
	if res != want {
		t.Errorf("result: got %+v; want %+v", res, want)
	}
	if n := countRows(t, sqlDB, `SELECT COUNT(*) FROM sessions WHERE namespace = 'ns-a'`); n != 0 {
		t.Errorf("post-delete sessions: want 0, got %d", n)
	}
}

func TestDeleteSessions_ClientFilter(t *testing.T) {
	store, sqlDB, teardown := openConcreteStore(t)
	defer teardown()

	seedClient(t, sqlDB, "ns-a", "alice")
	seedClient(t, sqlDB, "ns-a", "bob")
	seedSession(t, sqlDB, "ns-a", "alice", "a1", 60)
	seedSession(t, sqlDB, "ns-a", "alice", "a2", 60)
	seedSession(t, sqlDB, "ns-a", "bob", "b1", 60)

	res, err := store.Sessions().DeleteAll(context.Background(), db.ListSessionsFilter{Namespace: "ns-a", Client: "alice"})
	if err != nil {
		t.Fatalf("DeleteSessions: %v", err)
	}
	if res.DeletedSessions != 2 {
		t.Errorf("deleted: want 2 alice sessions, got %d", res.DeletedSessions)
	}
	if n := countRows(t, sqlDB, `SELECT COUNT(*) FROM sessions WHERE namespace = 'ns-a' AND client = 'bob'`); n != 1 {
		t.Errorf("bob's session intact: want 1, got %d", n)
	}
}

func TestDeleteSessions_LabelFilter(t *testing.T) {
	store, sqlDB, teardown := openConcreteStore(t)
	defer teardown()

	seedClient(t, sqlDB, "ns-a", db.DefaultClient)
	seedSession(t, sqlDB, "ns-a", db.DefaultClient, "prod-1", 60)
	seedSession(t, sqlDB, "ns-a", db.DefaultClient, "prod-2", 60)
	seedSession(t, sqlDB, "ns-a", db.DefaultClient, "dev-1", 60)
	seedSessionLabel(t, sqlDB, "ns-a", "prod-1", "env", "prod")
	seedSessionLabel(t, sqlDB, "ns-a", "prod-2", "env", "prod")
	seedSessionLabel(t, sqlDB, "ns-a", "dev-1", "env", "dev")

	res, err := store.Sessions().DeleteAll(context.Background(), db.ListSessionsFilter{
		Namespace: "ns-a",
		Labels:    map[string]string{"env": "prod"},
	})
	if err != nil {
		t.Fatalf("DeleteSessions: %v", err)
	}
	if res.DeletedSessions != 2 {
		t.Errorf("deleted: want 2 prod sessions, got %d", res.DeletedSessions)
	}
	if n := countRows(t, sqlDB, `SELECT COUNT(*) FROM sessions WHERE namespace = 'ns-a' AND session_id = 'dev-1'`); n != 1 {
		t.Errorf("dev session intact: want 1, got %d", n)
	}
}

func TestDeleteSessions_CrossNamespaceIsolation(t *testing.T) {
	store, sqlDB, teardown := openConcreteStore(t)
	defer teardown()

	seedClient(t, sqlDB, "ns-a", db.DefaultClient)
	seedClient(t, sqlDB, "ns-b", db.DefaultClient)
	seedSession(t, sqlDB, "ns-a", db.DefaultClient, "shared", 60)
	seedSession(t, sqlDB, "ns-b", db.DefaultClient, "shared", 60)

	res, err := store.Sessions().DeleteAll(context.Background(), db.ListSessionsFilter{Namespace: "ns-a"})
	if err != nil {
		t.Fatalf("DeleteSessions ns-a: %v", err)
	}
	if res.DeletedSessions != 1 {
		t.Errorf("deleted: want 1 ns-a session, got %d", res.DeletedSessions)
	}
	if n := countRows(t, sqlDB, `SELECT COUNT(*) FROM sessions WHERE namespace = 'ns-b' AND session_id = 'shared'`); n != 1 {
		t.Errorf("ns-b session intact: want 1, got %d", n)
	}
}

// ---------- ScanExpiredSessions ----------

func TestScanExpiredSessions_NoneExpired(t *testing.T) {
	store, sqlDB, teardown := openConcreteStore(t)
	defer teardown()

	seedClient(t, sqlDB, "ns-a", db.DefaultClient)
	seedSession(t, sqlDB, "ns-a", db.DefaultClient, "fresh", 60)

	got, err := store.Sessions().ScanExpired(context.Background(), time.Now())
	if err != nil {
		t.Fatalf("ScanExpiredSessions: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("want 0 expired, got %d", len(got))
	}
}

func TestScanExpiredSessions_MixedTTLs(t *testing.T) {
	store, sqlDB, teardown := openConcreteStore(t)
	defer teardown()

	seedClient(t, sqlDB, "ns-a", db.DefaultClient)
	now := time.Now().UTC()
	// fresh: activity now, TTL 60min → not expired
	seedSessionWithActivity(t, sqlDB, "ns-a", db.DefaultClient, "fresh", 60, now)
	// expired-recently: activity 10m ago, TTL 5min → expired
	seedSessionWithActivity(t, sqlDB, "ns-a", db.DefaultClient, "exp-recent", 5, now.Add(-10*time.Minute))
	// expired-long-ago: activity 2h ago, TTL 1min → expired (older)
	seedSessionWithActivity(t, sqlDB, "ns-a", db.DefaultClient, "exp-old", 1, now.Add(-2*time.Hour))

	got, err := store.Sessions().ScanExpired(context.Background(), now)
	if err != nil {
		t.Fatalf("ScanExpiredSessions: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("want 2 expired, got %d", len(got))
	}
	// Oldest first.
	if got[0].SessionID != "exp-old" || got[1].SessionID != "exp-recent" {
		t.Errorf("order: got [%q, %q], want [exp-old, exp-recent]", got[0].SessionID, got[1].SessionID)
	}
	for _, ref := range got {
		if ref.Namespace != "ns-a" {
			t.Errorf("ref %q: namespace got %q, want ns-a", ref.SessionID, ref.Namespace)
		}
	}
}

func TestScanExpiredSessions_CrossNamespace(t *testing.T) {
	store, sqlDB, teardown := openConcreteStore(t)
	defer teardown()

	seedClient(t, sqlDB, "ns-a", db.DefaultClient)
	seedClient(t, sqlDB, "ns-b", db.DefaultClient)
	past := time.Now().UTC().Add(-2 * time.Hour)
	seedSessionWithActivity(t, sqlDB, "ns-a", db.DefaultClient, "a-exp", 1, past)
	seedSessionWithActivity(t, sqlDB, "ns-b", db.DefaultClient, "b-exp", 1, past)

	got, err := store.Sessions().ScanExpired(context.Background(), time.Now())
	if err != nil {
		t.Fatalf("ScanExpiredSessions: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("want 2 expired across namespaces, got %d", len(got))
	}
	seen := map[string]string{}
	for _, ref := range got {
		seen[ref.SessionID] = ref.Namespace
	}
	if seen["a-exp"] != "ns-a" || seen["b-exp"] != "ns-b" {
		t.Errorf("namespace mapping: got %+v", seen)
	}
}

// ---------- session.Service + cache ----------
//
// Cache-hit proofs use raw-SQL DELETE between two Lookups: if the second
// Lookup returns metadata after the row is gone, it came from cache, not DB.

func deleteSessionRow(t *testing.T, sqlDB *sql.DB, namespace, id string) {
	t.Helper()
	if _, err := sqlDB.ExecContext(context.Background(),
		`DELETE FROM sessions WHERE namespace = $1 AND session_id = $2`,
		namespace, id,
	); err != nil {
		t.Fatalf("raw delete %q/%q: %v", namespace, id, err)
	}
}

func TestSessionService_Create_PrimesCache(t *testing.T) {
	store, sqlDB, teardown := openConcreteStore(t)
	defer teardown()

	svc := session.New(store, 10_000)
	if err := svc.Create(context.Background(), &db.Session{
		ID: "s1", Namespace: "ns-a", Client: "alice", TTLMinutes: 60,
	}); err != nil {
		t.Fatalf("Create: %v", err)
	}
	deleteSessionRow(t, sqlDB, "ns-a", "s1")

	md, err := svc.Lookup(context.Background(), "ns-a", "s1")
	if err != nil {
		t.Fatalf("Lookup after Create + raw-DELETE: want cached hit, got %v", err)
	}
	if md.Client != "alice" || md.TTLMinutes != 60 {
		t.Errorf("cached metadata: got %+v", md)
	}
	// Regression: input Session.CreatedAt is zero (column is DB-assigned via
	// DEFAULT now()); cache prime must use the DB-returned value, not the
	// input. Without the DAL's RETURNING flow this asserts as zero time.
	if md.CreatedAt.IsZero() {
		t.Error("cached CreatedAt is zero — Create did not propagate the DB-assigned timestamp into the cache")
	}
}

func TestSessionService_Lookup_MissPopulatesCache(t *testing.T) {
	store, sqlDB, teardown := openConcreteStore(t)
	defer teardown()

	seedClient(t, sqlDB, "ns-a", db.DefaultClient)
	seedSession(t, sqlDB, "ns-a", db.DefaultClient, "s1", 60)

	svc := session.New(store, 10_000)
	if _, err := svc.Lookup(context.Background(), "ns-a", "s1"); err != nil {
		t.Fatalf("first Lookup (populate): %v", err)
	}
	deleteSessionRow(t, sqlDB, "ns-a", "s1")

	md, err := svc.Lookup(context.Background(), "ns-a", "s1")
	if err != nil {
		t.Fatalf("Lookup after populate + raw-DELETE: want cached hit, got %v", err)
	}
	if md.Client != db.DefaultClient || md.TTLMinutes != 60 {
		t.Errorf("cached metadata: got %+v", md)
	}
}

func TestSessionService_Lookup_NotFoundNotCached(t *testing.T) {
	store, _, teardown := openConcreteStore(t)
	defer teardown()

	svc := session.New(store, 10_000)
	if _, err := svc.Lookup(context.Background(), "ns-a", "missing"); !errors.Is(err, db.ErrSessionNotFound) {
		t.Fatalf("first Lookup: want ErrSessionNotFound, got %v", err)
	}
	// Second call must also hit DB — negative caching is intentionally absent.
	if _, err := svc.Lookup(context.Background(), "ns-a", "missing"); !errors.Is(err, db.ErrSessionNotFound) {
		t.Fatalf("second Lookup: want ErrSessionNotFound, got %v", err)
	}
}

func TestSessionService_Delete_InvalidatesCache(t *testing.T) {
	store, _, teardown := openConcreteStore(t)
	defer teardown()

	svc := session.New(store, 10_000)
	if err := svc.Create(context.Background(), &db.Session{
		ID: "s1", Namespace: "ns-a", TTLMinutes: 60,
	}); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if _, err := svc.Delete(context.Background(), "ns-a", "s1"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := svc.Lookup(context.Background(), "ns-a", "s1"); !errors.Is(err, db.ErrSessionNotFound) {
		t.Errorf("Lookup after Delete: want ErrSessionNotFound, got %v", err)
	}
}

func TestSessionService_DeleteAll_InvalidatesOnlyTargetNamespace(t *testing.T) {
	store, sqlDB, teardown := openConcreteStore(t)
	defer teardown()

	svc := session.New(store, 10_000)
	for _, sid := range []string{"a1", "a2"} {
		if err := svc.Create(context.Background(), &db.Session{
			ID: sid, Namespace: "ns-a", TTLMinutes: 60,
		}); err != nil {
			t.Fatalf("Create ns-a/%s: %v", sid, err)
		}
	}
	if err := svc.Create(context.Background(), &db.Session{
		ID: "b1", Namespace: "ns-b", TTLMinutes: 60,
	}); err != nil {
		t.Fatalf("Create ns-b/b1: %v", err)
	}

	if _, err := svc.DeleteAll(context.Background(), db.ListSessionsFilter{Namespace: "ns-a"}); err != nil {
		t.Fatalf("DeleteAll: %v", err)
	}

	// ns-a entries: gone from cache + gone from DB.
	for _, sid := range []string{"a1", "a2"} {
		if _, err := svc.Lookup(context.Background(), "ns-a", sid); !errors.Is(err, db.ErrSessionNotFound) {
			t.Errorf("Lookup ns-a/%s after DeleteAll: want ErrSessionNotFound, got %v", sid, err)
		}
	}
	// ns-b entry: untouched; raw-DELETE proves it's served from cache.
	deleteSessionRow(t, sqlDB, "ns-b", "b1")
	if _, err := svc.Lookup(context.Background(), "ns-b", "b1"); err != nil {
		t.Errorf("Lookup ns-b/b1: cache should still hold it; got %v", err)
	}
}

func TestSessionService_Touch_OnStaleEntrySelfHeals(t *testing.T) {
	store, sqlDB, teardown := openConcreteStore(t)
	defer teardown()

	svc := session.New(store, 10_000)
	if err := svc.Create(context.Background(), &db.Session{
		ID: "s1", Namespace: "ns-a", TTLMinutes: 60,
	}); err != nil {
		t.Fatalf("Create: %v", err)
	}
	deleteSessionRow(t, sqlDB, "ns-a", "s1")

	// Touch goes to DB, gets ErrSessionNotFound, evicts the stale cache entry.
	if err := svc.Touch(context.Background(), "ns-a", "s1", time.Now()); !errors.Is(err, db.ErrSessionNotFound) {
		t.Fatalf("Touch on stale: want ErrSessionNotFound, got %v", err)
	}
	// Lookup must now miss + go to DB + return ErrSessionNotFound.
	if _, err := svc.Lookup(context.Background(), "ns-a", "s1"); !errors.Is(err, db.ErrSessionNotFound) {
		t.Errorf("Lookup after Touch self-heal: want ErrSessionNotFound, got %v", err)
	}
}

func TestSessionService_SameIDDifferentNamespacesIndependent(t *testing.T) {
	store, _, teardown := openConcreteStore(t)
	defer teardown()

	svc := session.New(store, 10_000)
	if err := svc.Create(context.Background(), &db.Session{
		ID: "shared", Namespace: "ns-a", Client: "alice", TTLMinutes: 60,
	}); err != nil {
		t.Fatalf("Create ns-a: %v", err)
	}
	if err := svc.Create(context.Background(), &db.Session{
		ID: "shared", Namespace: "ns-b", Client: "bob", TTLMinutes: 90,
	}); err != nil {
		t.Fatalf("Create ns-b: %v", err)
	}
	mdA, err := svc.Lookup(context.Background(), "ns-a", "shared")
	if err != nil {
		t.Fatalf("Lookup ns-a: %v", err)
	}
	mdB, err := svc.Lookup(context.Background(), "ns-b", "shared")
	if err != nil {
		t.Fatalf("Lookup ns-b: %v", err)
	}
	if mdA.Client != "alice" || mdA.TTLMinutes != 60 {
		t.Errorf("ns-a cache: got %+v", mdA)
	}
	if mdB.Client != "bob" || mdB.TTLMinutes != 90 {
		t.Errorf("ns-b cache: got %+v", mdB)
	}
}

func TestSessionService_CacheDisabled_AlwaysHitsDB(t *testing.T) {
	store, sqlDB, teardown := openConcreteStore(t)
	defer teardown()

	svc := session.New(store, 0) // disabled
	if err := svc.Create(context.Background(), &db.Session{
		ID: "s1", Namespace: "ns-a", TTLMinutes: 60,
	}); err != nil {
		t.Fatalf("Create: %v", err)
	}
	deleteSessionRow(t, sqlDB, "ns-a", "s1")
	if _, err := svc.Lookup(context.Background(), "ns-a", "s1"); !errors.Is(err, db.ErrSessionNotFound) {
		t.Errorf("Lookup with cache disabled: want DB miss → ErrSessionNotFound, got %v", err)
	}
}
