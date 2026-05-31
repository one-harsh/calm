// Copyright 2026 The CALM Authors
// SPDX-License-Identifier: Apache-2.0

package integration

import (
	"context"
	"database/sql"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	logging "github.com/one-harsh/context-logging"

	"github.com/one-harsh/calm/internal/auth"
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
	raw, err := auth.NewRandomToken()
	if err != nil {
		t.Fatalf("mint token: %v", err)
	}
	sess := &db.Session{
		Namespace:        "ns-a",
		Client:           db.DefaultClient,
		TTLMinutes:       60,
		SessionTokenHash: auth.HashToken("ns-a", raw),
	}
	if err := store.Sessions().Create(context.Background(), sess); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	if sess.ID <= 0 {
		t.Errorf("sess.ID = %d; want > 0 after RETURNING id", sess.ID)
	}
	got := countRows(t, sqlDB, `SELECT COUNT(*) FROM sessions WHERE id = $1`, sess.ID)
	if got != 1 {
		t.Errorf("want 1 session row, got %d", got)
	}
}

func TestCreateSession_HappyWithLabels(t *testing.T) {
	store, sqlDB, teardown := openConcreteStore(t)
	defer teardown()

	seedClient(t, sqlDB, "ns-a", db.DefaultClient)
	raw, err := auth.NewRandomToken()
	if err != nil {
		t.Fatalf("mint token: %v", err)
	}
	labels := map[string]string{"env": "prod", "team": "ml"}
	sess := &db.Session{
		Namespace:        "ns-a",
		Client:           db.DefaultClient,
		TTLMinutes:       60,
		SessionTokenHash: auth.HashToken("ns-a", raw),
		Labels:           labels,
	}
	if err := store.Sessions().Create(context.Background(), sess); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	got := countRows(t, sqlDB,
		`SELECT COUNT(*) FROM session_labels WHERE session_id = $1`,
		sess.ID,
	)
	if got != 2 {
		t.Errorf("want 2 label rows, got %d", got)
	}
}

// Post-WI-09c: clients are first-class entities. session.Service.Create no
// longer auto-registers — the client must exist beforehand. A missing
// client surfaces as ErrClientNotFound via the DAL's FK-violation
// translation.
func TestSessionService_Create_UnregisteredClientReturnsErrClientNotFound(t *testing.T) {
	store, sqlDB, teardown := openConcreteStore(t)
	defer teardown()

	svc := session.New(store, session.Config{CacheSize: 10_000})
	err := svc.Create(context.Background(), &db.Session{
		Namespace: "ns-a", Client: "alice", TTLMinutes: 60,
	}, "")
	if !errors.Is(err, db.ErrClientNotFound) {
		t.Fatalf("service Create with unregistered client: got %v; want ErrClientNotFound", err)
	}
	if n := countRows(t, sqlDB,
		`SELECT COUNT(*) FROM clients WHERE namespace = $1 AND name = $2`,
		"ns-a", "alice",
	); n != 0 {
		t.Errorf("client row count = %d; want 0 (service must not auto-register)", n)
	}
}

func TestSessionService_Create_EmptyClientDefaultsToDefault(t *testing.T) {
	store, sqlDB, teardown := openConcreteStore(t)
	defer teardown()
	seedClient(t, sqlDB, "ns-a", db.DefaultClient)

	svc := session.New(store, session.Config{CacheSize: 10_000})
	sess := &db.Session{Namespace: "ns-a", TTLMinutes: 60}
	if err := svc.Create(context.Background(), sess, ""); err != nil {
		t.Fatalf("service Create: %v", err)
	}
	// session.client column should be "default" (normalized in place).
	var got string
	if err := sqlDB.QueryRow(
		`SELECT client FROM sessions WHERE id = $1`, sess.ID,
	).Scan(&got); err != nil {
		t.Fatalf("read session.client: %v", err)
	}
	if got != db.DefaultClient {
		t.Errorf("session.client = %q; want %q", got, db.DefaultClient)
	}
}

func TestCreateSession_ExistingClientIdempotent(t *testing.T) {
	store, sqlDB, teardown := openConcreteStore(t)
	defer teardown()

	seedClient(t, sqlDB, "ns-a", "alice")
	raw, err := auth.NewRandomToken()
	if err != nil {
		t.Fatalf("mint token: %v", err)
	}
	if err := store.Sessions().Create(context.Background(), &db.Session{
		Namespace: "ns-a", Client: "alice", TTLMinutes: 60,
		SessionTokenHash: auth.HashToken("ns-a", raw),
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

func TestCreateSession_EmptySessionTokenHash(t *testing.T) {
	store, _, teardown := openConcreteStore(t)
	defer teardown()
	err := store.Sessions().Create(context.Background(), &db.Session{Namespace: "ns-a", TTLMinutes: 60})
	if !errors.Is(err, db.ErrSessionTokenHashRequired) {
		t.Errorf("want ErrSessionTokenHashRequired, got %v", err)
	}
}

func TestCreateSession_EmptyNamespace(t *testing.T) {
	store, _, teardown := openConcreteStore(t)
	defer teardown()
	raw, err := auth.NewRandomToken()
	if err != nil {
		t.Fatalf("mint token: %v", err)
	}
	err = store.Sessions().Create(context.Background(), &db.Session{
		TTLMinutes:       60,
		SessionTokenHash: auth.HashToken("", raw),
	})
	if !errors.Is(err, db.ErrNamespaceRequired) {
		t.Errorf("want ErrNamespaceRequired, got %v", err)
	}
}

// ---------- GetSession ----------

func TestGetSession_HappyNoLabels(t *testing.T) {
	store, sqlDB, teardown := openConcreteStore(t)
	defer teardown()

	seedClient(t, sqlDB, "ns-a", db.DefaultClient)
	seeded := seedSession(t, sqlDB, "ns-a", db.DefaultClient, 60)

	got, err := store.Sessions().Get(context.Background(), "ns-a", auth.HashToken("ns-a", seeded.SessionToken))
	if err != nil {
		t.Fatalf("GetSession: %v", err)
	}
	if got.ID != seeded.ID || got.Namespace != "ns-a" || got.Client != db.DefaultClient || got.TTLMinutes != 60 {
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
	seeded := seedSession(t, sqlDB, "ns-a", db.DefaultClient, 60)
	seedSessionLabel(t, sqlDB, seeded.ID, "env", "prod")
	seedSessionLabel(t, sqlDB, seeded.ID, "team", "ml")

	got, err := store.Sessions().Get(context.Background(), "ns-a", auth.HashToken("ns-a", seeded.SessionToken))
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
	_, err := store.Sessions().Get(context.Background(), "ns-a", auth.HashToken("ns-a", "missing"))
	if !errors.Is(err, db.ErrSessionNotFound) {
		t.Errorf("want ErrSessionNotFound, got %v", err)
	}
}

func TestGetSession_CrossNamespaceReturnsNotFound(t *testing.T) {
	store, sqlDB, teardown := openConcreteStore(t)
	defer teardown()

	seedClient(t, sqlDB, "ns-a", db.DefaultClient)
	seeded := seedSession(t, sqlDB, "ns-a", db.DefaultClient, 60)

	// Same raw token, different namespace → hash differs (namespace-scoped) and
	// even if it didn't, the WHERE namespace = $1 predicate excludes it.
	_, err := store.Sessions().Get(context.Background(), "ns-b", auth.HashToken("ns-b", seeded.SessionToken))
	if !errors.Is(err, db.ErrSessionNotFound) {
		t.Errorf("want ErrSessionNotFound (invisibility), got %v", err)
	}
}

func TestGetSession_SameNamespacesIndependent(t *testing.T) {
	store, sqlDB, teardown := openConcreteStore(t)
	defer teardown()

	seedClient(t, sqlDB, "ns-a", db.DefaultClient)
	seedClient(t, sqlDB, "ns-b", db.DefaultClient)
	seededA := seedSession(t, sqlDB, "ns-a", db.DefaultClient, 60)
	seededB := seedSession(t, sqlDB, "ns-b", db.DefaultClient, 90)
	seedSessionLabel(t, sqlDB, seededA.ID, "side", "a")
	seedSessionLabel(t, sqlDB, seededB.ID, "side", "b")

	got, err := store.Sessions().Get(context.Background(), "ns-a", auth.HashToken("ns-a", seededA.SessionToken))
	if err != nil {
		t.Fatalf("GetSession ns-a: %v", err)
	}
	if got.Namespace != "ns-a" || got.TTLMinutes != 60 || got.Labels["side"] != "a" {
		t.Errorf("ns-a session: got %+v", got)
	}
	got, err = store.Sessions().Get(context.Background(), "ns-b", auth.HashToken("ns-b", seededB.SessionToken))
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
	_, err := store.Sessions().Get(context.Background(), "", auth.HashToken("", "anything"))
	if !errors.Is(err, db.ErrNamespaceRequired) {
		t.Errorf("want ErrNamespaceRequired, got %v", err)
	}
}

func TestGetSession_EmptyHash(t *testing.T) {
	store, _, teardown := openConcreteStore(t)
	defer teardown()
	_, err := store.Sessions().Get(context.Background(), "ns-a", nil)
	if !errors.Is(err, db.ErrSessionTokenHashRequired) {
		t.Errorf("want ErrSessionTokenHashRequired, got %v", err)
	}
}

// ---------- TouchSession ----------

func TestTouchSession_AdvancesTimestamp(t *testing.T) {
	store, sqlDB, teardown := openConcreteStore(t)
	defer teardown()

	seedClient(t, sqlDB, "ns-a", db.DefaultClient)
	past := time.Now().Add(-1 * time.Hour).UTC()
	seeded := seedSessionWithActivity(t, sqlDB, "ns-a", db.DefaultClient, 60, past)

	future := time.Now().Add(1 * time.Hour).UTC()
	if err := store.Sessions().Touch(context.Background(), "ns-a", auth.HashToken("ns-a", seeded.SessionToken), future); err != nil {
		t.Fatalf("TouchSession: %v", err)
	}

	var got time.Time
	if err := sqlDB.QueryRowContext(context.Background(),
		`SELECT last_activity FROM sessions WHERE id = $1`, seeded.ID,
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
	seeded := seedSessionWithActivity(t, sqlDB, "ns-a", db.DefaultClient, 60, now)

	past := now.Add(-1 * time.Hour)
	if err := store.Sessions().Touch(context.Background(), "ns-a", auth.HashToken("ns-a", seeded.SessionToken), past); err != nil {
		t.Fatalf("TouchSession (past): %v", err)
	}
	var got time.Time
	if err := sqlDB.QueryRowContext(context.Background(),
		`SELECT last_activity FROM sessions WHERE id = $1`, seeded.ID,
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
	err := store.Sessions().Touch(context.Background(), "ns-a", auth.HashToken("ns-a", "missing"), time.Now())
	if !errors.Is(err, db.ErrSessionNotFound) {
		t.Errorf("want ErrSessionNotFound, got %v", err)
	}
}

func TestTouchSession_CrossNamespaceNotFound(t *testing.T) {
	store, sqlDB, teardown := openConcreteStore(t)
	defer teardown()
	seedClient(t, sqlDB, "ns-a", db.DefaultClient)
	seeded := seedSession(t, sqlDB, "ns-a", db.DefaultClient, 60)
	err := store.Sessions().Touch(context.Background(), "ns-b", auth.HashToken("ns-b", seeded.SessionToken), time.Now())
	if !errors.Is(err, db.ErrSessionNotFound) {
		t.Errorf("want ErrSessionNotFound, got %v", err)
	}
}

func TestTouchSession_EmptyNamespace(t *testing.T) {
	store, _, teardown := openConcreteStore(t)
	defer teardown()
	err := store.Sessions().Touch(context.Background(), "", auth.HashToken("", "anything"), time.Now())
	if !errors.Is(err, db.ErrNamespaceRequired) {
		t.Errorf("want ErrNamespaceRequired, got %v", err)
	}
}

func TestTouchSession_EmptyHash(t *testing.T) {
	store, _, teardown := openConcreteStore(t)
	defer teardown()
	err := store.Sessions().Touch(context.Background(), "ns-a", nil, time.Now())
	if !errors.Is(err, db.ErrSessionTokenHashRequired) {
		t.Errorf("want ErrSessionTokenHashRequired, got %v", err)
	}
}

// ---------- DeleteSession ----------

func TestDeleteSession_HappyPathFullCascade(t *testing.T) {
	store, sqlDB, teardown := openConcreteStore(t)
	defer teardown()

	seedClient(t, sqlDB, "ns-a", db.DefaultClient)
	seeded := seedSession(t, sqlDB, "ns-a", db.DefaultClient, 60)
	seedSessionLabel(t, sqlDB, seeded.ID, "env", "prod")
	seedSessionLabel(t, sqlDB, seeded.ID, "team", "ml")
	for _, src := range []string{"src-a", "src-b"} {
		sourceID := seedSource(t, sqlDB, seeded.ID, src)
		for j := 0; j < 3; j++ {
			seedChunk(t, sqlDB, sourceID, "title", "content", "prose")
		}
	}
	for j := 0; j < 4; j++ {
		seedEvent(t, sqlDB, seeded.ID, "tool_invocation"+string(rune('0'+j)), 3, []byte(`{}`))
	}
	seedVocab(t, sqlDB, seeded.ID, "alpha", 1)
	seedVocab(t, sqlDB, seeded.ID, "beta", 2)

	res, err := store.Sessions().Delete(context.Background(), "ns-a", auth.HashToken("ns-a", seeded.SessionToken))
	if err != nil {
		t.Fatalf("DeleteSession: %v", err)
	}

	want := db.DeleteSessionResult{
		ID:       seeded.ID,
		Cascaded: db.CascadeCounts{Sources: 2, Chunks: 6, Events: 4, Labels: 2},
	}
	if res != want {
		t.Errorf("result: got %+v; want %+v", res, want)
	}

	tables := []struct {
		name  string
		query string
	}{
		{"sessions", `SELECT COUNT(*) FROM sessions WHERE id = $1`},
		{"session_labels", `SELECT COUNT(*) FROM session_labels WHERE session_id = $1`},
		{"sources", `SELECT COUNT(*) FROM sources WHERE session_id = $1`},
		{"chunks", `SELECT COUNT(*) FROM chunks WHERE source_id IN (SELECT id FROM sources WHERE session_id = $1)`},
		{"session_events", `SELECT COUNT(*) FROM session_events WHERE session_id = $1`},
		{"vocabulary", `SELECT COUNT(*) FROM vocabulary WHERE session_id = $1`},
	}
	for _, tbl := range tables {
		if n := countRows(t, sqlDB, tbl.query, seeded.ID); n != 0 {
			t.Errorf("post-delete %s rows: want 0, got %d", tbl.name, n)
		}
	}
}

func TestDeleteSession_NotFound(t *testing.T) {
	store, _, teardown := openConcreteStore(t)
	defer teardown()
	_, err := store.Sessions().Delete(context.Background(), "ns-a", auth.HashToken("ns-a", "missing"))
	if !errors.Is(err, db.ErrSessionNotFound) {
		t.Errorf("want ErrSessionNotFound, got %v", err)
	}
}

func TestDeleteSession_CrossNamespaceNotFound(t *testing.T) {
	store, sqlDB, teardown := openConcreteStore(t)
	defer teardown()

	seedClient(t, sqlDB, "ns-a", db.DefaultClient)
	seeded := seedSession(t, sqlDB, "ns-a", db.DefaultClient, 60)

	_, err := store.Sessions().Delete(context.Background(), "ns-b", auth.HashToken("ns-b", seeded.SessionToken))
	if !errors.Is(err, db.ErrSessionNotFound) {
		t.Errorf("want ErrSessionNotFound, got %v", err)
	}
	if n := countRows(t, sqlDB, `SELECT COUNT(*) FROM sessions WHERE id = $1`, seeded.ID); n != 1 {
		t.Errorf("ns-a row should be untouched, got count %d", n)
	}
}

func TestDeleteSession_SameClientDifferentNamespaceIsolated(t *testing.T) {
	store, sqlDB, teardown := openConcreteStore(t)
	defer teardown()

	seedClient(t, sqlDB, "ns-a", db.DefaultClient)
	seedClient(t, sqlDB, "ns-b", db.DefaultClient)
	seededA := seedSession(t, sqlDB, "ns-a", db.DefaultClient, 60)
	seededB := seedSession(t, sqlDB, "ns-b", db.DefaultClient, 60)
	seedSessionLabel(t, sqlDB, seededA.ID, "side", "a")
	seedSessionLabel(t, sqlDB, seededB.ID, "side", "b")
	srcB := seedSource(t, sqlDB, seededB.ID, "src")
	seedChunk(t, sqlDB, srcB, "title", "content", "prose")

	if _, err := store.Sessions().Delete(context.Background(), "ns-a", auth.HashToken("ns-a", seededA.SessionToken)); err != nil {
		t.Fatalf("DeleteSession ns-a: %v", err)
	}

	if n := countRows(t, sqlDB, `SELECT COUNT(*) FROM sessions WHERE id = $1`, seededB.ID); n != 1 {
		t.Errorf("ns-b session lost: want 1, got %d", n)
	}
	if n := countRows(t, sqlDB, `SELECT COUNT(*) FROM session_labels WHERE session_id = $1`, seededB.ID); n != 1 {
		t.Errorf("ns-b label lost: want 1, got %d", n)
	}
	if n := countRows(t, sqlDB, `SELECT COUNT(*) FROM sources WHERE session_id = $1`, seededB.ID); n != 1 {
		t.Errorf("ns-b source lost: want 1, got %d", n)
	}
}

func TestDeleteSession_NoChildrenCleanDelete(t *testing.T) {
	store, sqlDB, teardown := openConcreteStore(t)
	defer teardown()

	seedClient(t, sqlDB, "ns-a", db.DefaultClient)
	seeded := seedSession(t, sqlDB, "ns-a", db.DefaultClient, 60)

	res, err := store.Sessions().Delete(context.Background(), "ns-a", auth.HashToken("ns-a", seeded.SessionToken))
	if err != nil {
		t.Fatalf("DeleteSession: %v", err)
	}
	want := db.DeleteSessionResult{ID: seeded.ID}
	if res != want {
		t.Errorf("result: got %+v; want %+v", res, want)
	}
}

func TestDeleteSession_BumpsClientLastActivityAt(t *testing.T) {
	// HLD explicit-close requirement: Delete is itself activity on the client,
	// so clients.last_activity_at advances to the close moment.
	store, sqlDB, teardown := openConcreteStore(t)
	defer teardown()

	seedClient(t, sqlDB, "ns-a", db.DefaultClient)
	seeded := seedSession(t, sqlDB, "ns-a", db.DefaultClient, 60)

	before := time.Now().UTC()
	if _, err := store.Sessions().Delete(context.Background(), "ns-a", auth.HashToken("ns-a", seeded.SessionToken)); err != nil {
		t.Fatalf("DeleteSession: %v", err)
	}
	after := time.Now().UTC()

	var got sql.NullTime
	if err := sqlDB.QueryRow(
		`SELECT last_activity_at FROM clients WHERE namespace = $1 AND name = $2`,
		"ns-a", db.DefaultClient,
	).Scan(&got); err != nil {
		t.Fatalf("read client last_activity_at: %v", err)
	}
	if !got.Valid {
		t.Fatal("clients.last_activity_at still NULL after delete; want close timestamp")
	}
	if got.Time.Before(before) || got.Time.After(after) {
		t.Errorf("clients.last_activity_at = %v; want within [%v, %v] (close moment)", got.Time, before, after)
	}
}

func TestDeleteByID_ClientLastActivityFollowsSessionNotScanTime(t *testing.T) {
	// Scanner-triggered delete is NOT client activity — the workload did
	// nothing; the TTL scanner ran. clients.last_activity_at must carry the
	// session's own last_activity forward, not the scan moment, so operators
	// reading the management API can still distinguish dead clients from
	// active ones after all their sessions have been TTL-reaped.
	store, sqlDB, teardown := openConcreteStore(t)
	defer teardown()

	seedClient(t, sqlDB, "ns-a", db.DefaultClient)
	// Seed a session whose last_activity is well in the past.
	sessionAge := time.Now().UTC().Add(-2 * time.Hour).Round(time.Microsecond)
	seeded := seedSessionWithActivity(t, sqlDB, "ns-a", db.DefaultClient, 60, sessionAge)

	scanMoment := time.Now().UTC()
	if _, err := store.Sessions().DeleteByID(context.Background(), "ns-a", seeded.ID); err != nil {
		t.Fatalf("DeleteByID: %v", err)
	}

	var got sql.NullTime
	if err := sqlDB.QueryRow(
		`SELECT last_activity_at FROM clients WHERE namespace = $1 AND name = $2`,
		"ns-a", db.DefaultClient,
	).Scan(&got); err != nil {
		t.Fatalf("read client last_activity_at: %v", err)
	}
	if !got.Valid {
		t.Fatal("clients.last_activity_at still NULL after DeleteByID; want the session's last_activity")
	}
	// The bump should reflect the session's own activity (sessionAge), not
	// the scan moment. Allow a microsecond tolerance for DB precision.
	if delta := got.Time.Sub(sessionAge); delta < -time.Microsecond || delta > time.Microsecond {
		t.Errorf("clients.last_activity_at = %v; want = sessionAge %v (not scan moment %v)",
			got.Time, sessionAge, scanMoment)
	}
	if !got.Time.Before(scanMoment) {
		t.Errorf("clients.last_activity_at = %v is at-or-after scanMoment %v; scanner-triggered delete leaked scan time as activity",
			got.Time, scanMoment)
	}
}

func TestDeleteSession_MonotonicClientLastActivityAt(t *testing.T) {
	// GREATEST guards against tx-start-order racing commit-order: a delete
	// whose tx started earlier must not stamp clients.last_activity_at with
	// its older NOW() over a sibling's later bump. We simulate the hazard
	// by pre-seeding a future value that the delete must not regress.
	store, sqlDB, teardown := openConcreteStore(t)
	defer teardown()

	seedClient(t, sqlDB, "ns-a", db.DefaultClient)
	seeded := seedSession(t, sqlDB, "ns-a", db.DefaultClient, 60)

	future := time.Now().UTC().Add(1 * time.Hour).Round(time.Microsecond)
	if _, err := sqlDB.Exec(
		`UPDATE clients SET last_activity_at = $3 WHERE namespace = $1 AND name = $2`,
		"ns-a", db.DefaultClient, future,
	); err != nil {
		t.Fatalf("pre-seed last_activity_at: %v", err)
	}

	if _, err := store.Sessions().Delete(context.Background(), "ns-a", auth.HashToken("ns-a", seeded.SessionToken)); err != nil {
		t.Fatalf("DeleteSession: %v", err)
	}

	var got sql.NullTime
	if err := sqlDB.QueryRow(
		`SELECT last_activity_at FROM clients WHERE namespace = $1 AND name = $2`,
		"ns-a", db.DefaultClient,
	).Scan(&got); err != nil {
		t.Fatalf("read last_activity_at: %v", err)
	}
	if !got.Valid || !got.Time.Equal(future) {
		t.Errorf("clients.last_activity_at = %v; want %v (must not regress)", got.Time, future)
	}
}

func TestDeleteSession_NegativeIsolation(t *testing.T) {
	store, sqlDB, teardown := openConcreteStore(t)
	defer teardown()

	seedClient(t, sqlDB, "ns-a", db.DefaultClient)
	seededA := seedSession(t, sqlDB, "ns-a", db.DefaultClient, 60)
	seededB := seedSession(t, sqlDB, "ns-a", db.DefaultClient, 60)
	srcA := seedSource(t, sqlDB, seededA.ID, "a")
	seedChunk(t, sqlDB, srcA, "ta", "ca", "prose")
	srcB := seedSource(t, sqlDB, seededB.ID, "b")
	seedChunk(t, sqlDB, srcB, "tb", "cb", "prose")

	if _, err := store.Sessions().Delete(context.Background(), "ns-a", auth.HashToken("ns-a", seededA.SessionToken)); err != nil {
		t.Fatalf("DeleteSession sess-A: %v", err)
	}

	if n := countRows(t, sqlDB, `SELECT COUNT(*) FROM sources WHERE session_id = $1`, seededB.ID); n != 1 {
		t.Errorf("sess-B sources lost: want 1, got %d", n)
	}
	if n := countRows(t, sqlDB, `SELECT COUNT(*) FROM chunks WHERE source_id = $1`, srcB); n != 1 {
		t.Errorf("sess-B chunks lost: want 1, got %d", n)
	}
}

func TestDeleteSession_EmptyNamespace(t *testing.T) {
	store, _, teardown := openConcreteStore(t)
	defer teardown()
	_, err := store.Sessions().Delete(context.Background(), "", auth.HashToken("", "anything"))
	if !errors.Is(err, db.ErrNamespaceRequired) {
		t.Errorf("want ErrNamespaceRequired, got %v", err)
	}
}

func TestDeleteSession_EmptyHash(t *testing.T) {
	store, _, teardown := openConcreteStore(t)
	defer teardown()
	_, err := store.Sessions().Delete(context.Background(), "ns-a", nil)
	if !errors.Is(err, db.ErrSessionTokenHashRequired) {
		t.Errorf("want ErrSessionTokenHashRequired, got %v", err)
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
	seeded := seedSession(t, sqlDB, "ns-a", db.DefaultClient, 60)

	got, err := store.Sessions().List(context.Background(), db.ListSessionsFilter{Namespace: "ns-a"})
	if err != nil {
		t.Fatalf("ListManagedSessions: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("want 1 session, got %d", len(got))
	}
	if got[0].ID != seeded.ID || got[0].EventCount != 0 || got[0].Labels != nil {
		t.Errorf("session: got %+v", got[0])
	}
}

func TestListManagedSessions_LabelsAndEventCount(t *testing.T) {
	store, sqlDB, teardown := openConcreteStore(t)
	defer teardown()

	seedClient(t, sqlDB, "ns-a", db.DefaultClient)
	seeded := seedSession(t, sqlDB, "ns-a", db.DefaultClient, 60)
	seedSessionLabel(t, sqlDB, seeded.ID, "env", "prod")
	seedSessionLabel(t, sqlDB, seeded.ID, "team", "ml")
	seedSessionLabel(t, sqlDB, seeded.ID, "region", "us")
	for j := 0; j < 5; j++ {
		seedEvent(t, sqlDB, seeded.ID, "evt"+string(rune('0'+j)), 3, []byte(`{}`))
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
	oldS := seedSessionWithActivity(t, sqlDB, "ns-a", db.DefaultClient, 60, base.Add(-2*time.Hour))
	newest := seedSessionWithActivity(t, sqlDB, "ns-a", db.DefaultClient, 60, base)
	middle := seedSessionWithActivity(t, sqlDB, "ns-a", db.DefaultClient, 60, base.Add(-1*time.Hour))

	got, err := store.Sessions().List(context.Background(), db.ListSessionsFilter{Namespace: "ns-a"})
	if err != nil {
		t.Fatalf("ListManagedSessions: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("want 3 sessions, got %d", len(got))
	}
	wantOrder := []int64{newest.ID, middle.ID, oldS.ID}
	for i, w := range wantOrder {
		if got[i].ID != w {
			t.Errorf("position %d: want id=%d, got id=%d", i, w, got[i].ID)
		}
	}
}

func TestListManagedSessions_ClientFilter(t *testing.T) {
	store, sqlDB, teardown := openConcreteStore(t)
	defer teardown()

	seedClient(t, sqlDB, "ns-a", "alice")
	seedClient(t, sqlDB, "ns-a", "bob")
	seedSession(t, sqlDB, "ns-a", "alice", 60)
	seedSession(t, sqlDB, "ns-a", "alice", 60)
	seedSession(t, sqlDB, "ns-a", "bob", 60)

	got, err := store.Sessions().List(context.Background(), db.ListSessionsFilter{Namespace: "ns-a", Client: "alice"})
	if err != nil {
		t.Fatalf("ListManagedSessions: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("want 2 alice sessions, got %d", len(got))
	}
	for _, s := range got {
		if s.Client != "alice" {
			t.Errorf("session id=%d: client got %q, want alice", s.ID, s.Client)
		}
	}
}

func TestListManagedSessions_LabelFilterSingleKey(t *testing.T) {
	store, sqlDB, teardown := openConcreteStore(t)
	defer teardown()

	seedClient(t, sqlDB, "ns-a", db.DefaultClient)
	match := seedSession(t, sqlDB, "ns-a", db.DefaultClient, 60)
	nomatch := seedSession(t, sqlDB, "ns-a", db.DefaultClient, 60)
	seedSessionLabel(t, sqlDB, match.ID, "env", "prod")
	seedSessionLabel(t, sqlDB, nomatch.ID, "env", "dev")

	got, err := store.Sessions().List(context.Background(), db.ListSessionsFilter{
		Namespace: "ns-a",
		Labels:    map[string]string{"env": "prod"},
	})
	if err != nil {
		t.Fatalf("ListManagedSessions: %v", err)
	}
	if len(got) != 1 || got[0].ID != match.ID {
		t.Errorf("want [match=%d], got %+v", match.ID, got)
	}
}

func TestListManagedSessions_LabelFilterMultiKeyAND(t *testing.T) {
	store, sqlDB, teardown := openConcreteStore(t)
	defer teardown()

	seedClient(t, sqlDB, "ns-a", db.DefaultClient)
	both := seedSession(t, sqlDB, "ns-a", db.DefaultClient, 60)
	onlyenv := seedSession(t, sqlDB, "ns-a", db.DefaultClient, 60)
	seedSessionLabel(t, sqlDB, both.ID, "env", "prod")
	seedSessionLabel(t, sqlDB, both.ID, "region", "us")
	seedSessionLabel(t, sqlDB, onlyenv.ID, "env", "prod")

	got, err := store.Sessions().List(context.Background(), db.ListSessionsFilter{
		Namespace: "ns-a",
		Labels:    map[string]string{"env": "prod", "region": "us"},
	})
	if err != nil {
		t.Fatalf("ListManagedSessions: %v", err)
	}
	if len(got) != 1 || got[0].ID != both.ID {
		t.Errorf("want [both=%d] (both labels match), got %+v", both.ID, got)
	}
}

func TestListManagedSessions_CrossNamespaceIsolation(t *testing.T) {
	store, sqlDB, teardown := openConcreteStore(t)
	defer teardown()

	seedClient(t, sqlDB, "ns-a", db.DefaultClient)
	seedClient(t, sqlDB, "ns-b", db.DefaultClient)
	seedSession(t, sqlDB, "ns-a", db.DefaultClient, 60)
	seedSession(t, sqlDB, "ns-b", db.DefaultClient, 60)
	seedSession(t, sqlDB, "ns-b", db.DefaultClient, 60)

	got, err := store.Sessions().List(context.Background(), db.ListSessionsFilter{Namespace: "ns-b"})
	if err != nil {
		t.Fatalf("ListManagedSessions ns-b: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("want 2 ns-b sessions, got %d", len(got))
	}
	for _, s := range got {
		if s.Namespace != "ns-b" {
			t.Errorf("session id=%d: namespace got %q, want ns-b", s.ID, s.Namespace)
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
		s := seedSession(t, sqlDB, "ns-a", "alice", 60)
		seedSessionLabel(t, sqlDB, s.ID, "env", "prod")
	}
	seedSession(t, sqlDB, "ns-a", "bob", 60)

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
	seedSession(t, sqlDB, "ns-a", db.DefaultClient, 60)
	seedSession(t, sqlDB, "ns-a", db.DefaultClient, 60)
	seedSession(t, sqlDB, "ns-b", db.DefaultClient, 60)

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
	for i := 0; i < 3; i++ {
		s := seedSession(t, sqlDB, "ns-a", db.DefaultClient, 60)
		seedSessionLabel(t, sqlDB, s.ID, "env", "prod")
		srcID := seedSource(t, sqlDB, s.ID, "src")
		seedChunk(t, sqlDB, srcID, "t", "c", "prose")
		seedChunk(t, sqlDB, srcID, "t", "c", "prose")
		seedEvent(t, sqlDB, s.ID, "evt", 3, []byte(`{}`))
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
	seedSession(t, sqlDB, "ns-a", "alice", 60)
	seedSession(t, sqlDB, "ns-a", "alice", 60)
	seedSession(t, sqlDB, "ns-a", "bob", 60)

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
	prod1 := seedSession(t, sqlDB, "ns-a", db.DefaultClient, 60)
	prod2 := seedSession(t, sqlDB, "ns-a", db.DefaultClient, 60)
	dev1 := seedSession(t, sqlDB, "ns-a", db.DefaultClient, 60)
	seedSessionLabel(t, sqlDB, prod1.ID, "env", "prod")
	seedSessionLabel(t, sqlDB, prod2.ID, "env", "prod")
	seedSessionLabel(t, sqlDB, dev1.ID, "env", "dev")

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
	if n := countRows(t, sqlDB, `SELECT COUNT(*) FROM sessions WHERE id = $1`, dev1.ID); n != 1 {
		t.Errorf("dev session intact: want 1, got %d", n)
	}
}

func TestDeleteSessions_CrossNamespaceIsolation(t *testing.T) {
	store, sqlDB, teardown := openConcreteStore(t)
	defer teardown()

	seedClient(t, sqlDB, "ns-a", db.DefaultClient)
	seedClient(t, sqlDB, "ns-b", db.DefaultClient)
	seedSession(t, sqlDB, "ns-a", db.DefaultClient, 60)
	seededB := seedSession(t, sqlDB, "ns-b", db.DefaultClient, 60)

	res, err := store.Sessions().DeleteAll(context.Background(), db.ListSessionsFilter{Namespace: "ns-a"})
	if err != nil {
		t.Fatalf("DeleteSessions ns-a: %v", err)
	}
	if res.DeletedSessions != 1 {
		t.Errorf("deleted: want 1 ns-a session, got %d", res.DeletedSessions)
	}
	if n := countRows(t, sqlDB, `SELECT COUNT(*) FROM sessions WHERE id = $1`, seededB.ID); n != 1 {
		t.Errorf("ns-b session intact: want 1, got %d", n)
	}
}

// ---------- ScanExpiredSessions ----------

func TestScanExpiredSessions_NoneExpired(t *testing.T) {
	store, sqlDB, teardown := openConcreteStore(t)
	defer teardown()

	seedClient(t, sqlDB, "ns-a", db.DefaultClient)
	seedSession(t, sqlDB, "ns-a", db.DefaultClient, 60)

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
	seedSessionWithActivity(t, sqlDB, "ns-a", db.DefaultClient, 60, now)
	// expired-recently: activity 10m ago, TTL 5min → expired
	expRecent := seedSessionWithActivity(t, sqlDB, "ns-a", db.DefaultClient, 5, now.Add(-10*time.Minute))
	// expired-long-ago: activity 2h ago, TTL 1min → expired (older)
	expOld := seedSessionWithActivity(t, sqlDB, "ns-a", db.DefaultClient, 1, now.Add(-2*time.Hour))

	got, err := store.Sessions().ScanExpired(context.Background(), now)
	if err != nil {
		t.Fatalf("ScanExpiredSessions: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("want 2 expired, got %d", len(got))
	}
	// Oldest first.
	if got[0].ID != expOld.ID || got[1].ID != expRecent.ID {
		t.Errorf("order: got [%d, %d], want [%d (old), %d (recent)]",
			got[0].ID, got[1].ID, expOld.ID, expRecent.ID)
	}
	for _, ref := range got {
		if ref.Namespace != "ns-a" {
			t.Errorf("ref id=%d: namespace got %q, want ns-a", ref.ID, ref.Namespace)
		}
	}
}

func TestScanExpiredSessions_CrossNamespace(t *testing.T) {
	store, sqlDB, teardown := openConcreteStore(t)
	defer teardown()

	seedClient(t, sqlDB, "ns-a", db.DefaultClient)
	seedClient(t, sqlDB, "ns-b", db.DefaultClient)
	past := time.Now().UTC().Add(-2 * time.Hour)
	expA := seedSessionWithActivity(t, sqlDB, "ns-a", db.DefaultClient, 1, past)
	expB := seedSessionWithActivity(t, sqlDB, "ns-b", db.DefaultClient, 1, past)

	got, err := store.Sessions().ScanExpired(context.Background(), time.Now())
	if err != nil {
		t.Fatalf("ScanExpiredSessions: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("want 2 expired across namespaces, got %d", len(got))
	}
	seen := map[int64]string{}
	for _, ref := range got {
		seen[ref.ID] = ref.Namespace
	}
	if seen[expA.ID] != "ns-a" || seen[expB.ID] != "ns-b" {
		t.Errorf("namespace mapping: got %+v (expected %d→ns-a, %d→ns-b)", seen, expA.ID, expB.ID)
	}
}

// ---------- session.Service + cache ----------
//
// Cache-hit proofs use raw-SQL DELETE between two Lookups: if the second
// Lookup returns metadata after the row is gone, it came from cache, not DB.

func deleteSessionRowByID(t *testing.T, sqlDB *sql.DB, id int64) {
	t.Helper()
	if _, err := sqlDB.ExecContext(context.Background(),
		`DELETE FROM sessions WHERE id = $1`, id,
	); err != nil {
		t.Fatalf("raw delete id=%d: %v", id, err)
	}
}

func TestSessionService_Create_PrimesCache(t *testing.T) {
	store, sqlDB, teardown := openConcreteStore(t)
	defer teardown()
	seedClient(t, sqlDB, "ns-a", "alice")

	svc := session.New(store, session.Config{CacheSize: 10_000})
	sess := &db.Session{Namespace: "ns-a", Client: "alice", TTLMinutes: 60}
	if err := svc.Create(context.Background(), sess, ""); err != nil {
		t.Fatalf("Create: %v", err)
	}
	deleteSessionRowByID(t, sqlDB, sess.ID)

	md, err := svc.Lookup(context.Background(), "ns-a", sess.SessionToken)
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
	seeded := seedSession(t, sqlDB, "ns-a", db.DefaultClient, 60)

	svc := session.New(store, session.Config{CacheSize: 10_000})
	if _, err := svc.Lookup(context.Background(), "ns-a", seeded.SessionToken); err != nil {
		t.Fatalf("first Lookup (populate): %v", err)
	}
	deleteSessionRowByID(t, sqlDB, seeded.ID)

	md, err := svc.Lookup(context.Background(), "ns-a", seeded.SessionToken)
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

	svc := session.New(store, session.Config{CacheSize: 10_000})
	if _, err := svc.Lookup(context.Background(), "ns-a", "missing"); !errors.Is(err, db.ErrSessionNotFound) {
		t.Fatalf("first Lookup: want ErrSessionNotFound, got %v", err)
	}
	// Second call must also hit DB — negative caching is intentionally absent.
	if _, err := svc.Lookup(context.Background(), "ns-a", "missing"); !errors.Is(err, db.ErrSessionNotFound) {
		t.Fatalf("second Lookup: want ErrSessionNotFound, got %v", err)
	}
}

func TestSessionService_Delete_InvalidatesCache(t *testing.T) {
	store, sqlDB, teardown := openConcreteStore(t)
	defer teardown()
	seedClient(t, sqlDB, "ns-a", db.DefaultClient)

	svc := session.New(store, session.Config{CacheSize: 10_000})
	sess := &db.Session{Namespace: "ns-a", TTLMinutes: 60}
	if err := svc.Create(context.Background(), sess, ""); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if _, err := svc.Delete(context.Background(), "ns-a", sess.SessionToken); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := svc.Lookup(context.Background(), "ns-a", sess.SessionToken); !errors.Is(err, db.ErrSessionNotFound) {
		t.Errorf("Lookup after Delete: want ErrSessionNotFound, got %v", err)
	}
}

func TestSessionService_DeleteAll_InvalidatesOnlyTargetNamespace(t *testing.T) {
	store, sqlDB, teardown := openConcreteStore(t)
	defer teardown()
	seedClient(t, sqlDB, "ns-a", db.DefaultClient)
	seedClient(t, sqlDB, "ns-b", db.DefaultClient)

	svc := session.New(store, session.Config{CacheSize: 10_000})
	var aTokens []string
	for i := 0; i < 2; i++ {
		s := &db.Session{Namespace: "ns-a", TTLMinutes: 60}
		if err := svc.Create(context.Background(), s, ""); err != nil {
			t.Fatalf("Create ns-a/#%d: %v", i, err)
		}
		aTokens = append(aTokens, s.SessionToken)
	}
	bSess := &db.Session{Namespace: "ns-b", TTLMinutes: 60}
	if err := svc.Create(context.Background(), bSess, ""); err != nil {
		t.Fatalf("Create ns-b/b1: %v", err)
	}

	if _, err := svc.DeleteAll(context.Background(), db.ListSessionsFilter{Namespace: "ns-a"}); err != nil {
		t.Fatalf("DeleteAll: %v", err)
	}

	// ns-a entries: gone from cache + gone from DB.
	for i, tok := range aTokens {
		if _, err := svc.Lookup(context.Background(), "ns-a", tok); !errors.Is(err, db.ErrSessionNotFound) {
			t.Errorf("Lookup ns-a/#%d after DeleteAll: want ErrSessionNotFound, got %v", i, err)
		}
	}
	// ns-b entry: untouched; raw-DELETE proves it's served from cache.
	deleteSessionRowByID(t, sqlDB, bSess.ID)
	if _, err := svc.Lookup(context.Background(), "ns-b", bSess.SessionToken); err != nil {
		t.Errorf("Lookup ns-b/b1: cache should still hold it; got %v", err)
	}
}

func TestSessionService_Touch_OnStaleEntrySelfHeals(t *testing.T) {
	store, sqlDB, teardown := openConcreteStore(t)
	defer teardown()
	seedClient(t, sqlDB, "ns-a", db.DefaultClient)

	svc := session.New(store, session.Config{CacheSize: 10_000})
	sess := &db.Session{Namespace: "ns-a", TTLMinutes: 60}
	if err := svc.Create(context.Background(), sess, ""); err != nil {
		t.Fatalf("Create: %v", err)
	}
	deleteSessionRowByID(t, sqlDB, sess.ID)

	// Touch goes to DB, gets ErrSessionNotFound, evicts the stale cache entry.
	if err := svc.Touch(context.Background(), "ns-a", sess.SessionToken, time.Now()); !errors.Is(err, db.ErrSessionNotFound) {
		t.Fatalf("Touch on stale: want ErrSessionNotFound, got %v", err)
	}
	// Lookup must now miss + go to DB + return ErrSessionNotFound.
	if _, err := svc.Lookup(context.Background(), "ns-a", sess.SessionToken); !errors.Is(err, db.ErrSessionNotFound) {
		t.Errorf("Lookup after Touch self-heal: want ErrSessionNotFound, got %v", err)
	}
}

func TestSessionService_SeparateNamespacesIndependent(t *testing.T) {
	store, sqlDB, teardown := openConcreteStore(t)
	defer teardown()
	seedClient(t, sqlDB, "ns-a", "alice")
	seedClient(t, sqlDB, "ns-b", "bob")

	svc := session.New(store, session.Config{CacheSize: 10_000})
	sessA := &db.Session{Namespace: "ns-a", Client: "alice", TTLMinutes: 60}
	if err := svc.Create(context.Background(), sessA, ""); err != nil {
		t.Fatalf("Create ns-a: %v", err)
	}
	sessB := &db.Session{Namespace: "ns-b", Client: "bob", TTLMinutes: 90}
	if err := svc.Create(context.Background(), sessB, ""); err != nil {
		t.Fatalf("Create ns-b: %v", err)
	}
	mdA, err := svc.Lookup(context.Background(), "ns-a", sessA.SessionToken)
	if err != nil {
		t.Fatalf("Lookup ns-a: %v", err)
	}
	mdB, err := svc.Lookup(context.Background(), "ns-b", sessB.SessionToken)
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
	seedClient(t, sqlDB, "ns-a", db.DefaultClient)

	svc := session.New(store, session.Config{}) // cache disabled
	sess := &db.Session{Namespace: "ns-a", TTLMinutes: 60}
	if err := svc.Create(context.Background(), sess, ""); err != nil {
		t.Fatalf("Create: %v", err)
	}
	deleteSessionRowByID(t, sqlDB, sess.ID)
	if _, err := svc.Lookup(context.Background(), "ns-a", sess.SessionToken); !errors.Is(err, db.ErrSessionNotFound) {
		t.Errorf("Lookup with cache disabled: want DB miss → ErrSessionNotFound, got %v", err)
	}
}

// ---------- expires_at column wiring (WI-06) ----------

// expires_at is maintained by a BEFORE trigger on the sessions table. These
// tests assert the column is populated end-to-end through the DAL — Create
// surfaces it via RETURNING; Get re-reads it; Touch causes the trigger to
// recompute it.

func TestSession_ExpiresAtPopulatedOnCreate(t *testing.T) {
	store, sqlDB, teardown := openConcreteStore(t)
	defer teardown()
	seedClient(t, sqlDB, "ns-a", db.DefaultClient)

	svc := session.New(store, session.Config{})
	sess := &db.Session{Namespace: "ns-a", TTLMinutes: 60}
	if err := svc.Create(context.Background(), sess, ""); err != nil {
		t.Fatalf("Create: %v", err)
	}
	want := sess.LastActivity.Add(60 * time.Minute)
	if d := sess.ExpiresAt.Sub(want).Abs(); d > 100*time.Millisecond {
		t.Errorf("ExpiresAt = %v; want ~%v (last_activity + 60m); diff = %v",
			sess.ExpiresAt, want, d)
	}
}

func TestSession_ExpiresAtPopulatedOnGet(t *testing.T) {
	store, sqlDB, teardown := openConcreteStore(t)
	defer teardown()

	seedClient(t, sqlDB, "ns-a", db.DefaultClient)
	seeded := seedSession(t, sqlDB, "ns-a", db.DefaultClient, 30)

	svc := session.New(store, session.Config{})
	got, err := svc.Get(context.Background(), "ns-a", seeded.SessionToken)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.ExpiresAt.IsZero() {
		t.Error("ExpiresAt is zero — Get's SELECT/Scan must populate it")
	}
	want := got.LastActivity.Add(30 * time.Minute)
	if d := got.ExpiresAt.Sub(want).Abs(); d > 100*time.Millisecond {
		t.Errorf("ExpiresAt = %v; want ~%v (last_activity + 30m)", got.ExpiresAt, want)
	}
}

func TestSession_ExpiresAtRecomputesOnTouch(t *testing.T) {
	store, sqlDB, teardown := openConcreteStore(t)
	defer teardown()
	seedClient(t, sqlDB, "ns-a", db.DefaultClient)

	svc := session.New(store, session.Config{})
	sess := &db.Session{Namespace: "ns-a", TTLMinutes: 30}
	if err := svc.Create(context.Background(), sess, ""); err != nil {
		t.Fatalf("Create: %v", err)
	}
	initial := sess.ExpiresAt

	// Touch with a later timestamp; the trigger must recompute expires_at.
	later := sess.LastActivity.Add(5 * time.Minute)
	if err := svc.Touch(context.Background(), "ns-a", sess.SessionToken, later); err != nil {
		t.Fatalf("Touch: %v", err)
	}
	got, err := svc.Get(context.Background(), "ns-a", sess.SessionToken)
	if err != nil {
		t.Fatalf("Get post-Touch: %v", err)
	}
	if !got.ExpiresAt.After(initial) {
		t.Errorf("ExpiresAt did not advance: initial=%v, post-touch=%v", initial, got.ExpiresAt)
	}
}

// ---------- TTL scanner (WI-06) ----------

// These tests spin up a real session.Scanner against the real DAL with a
// short interval (20 ms) so the scan happens before the test budget runs out.
// The scanner exits on context cancel; tests cancel + wait on a done channel.

func TestTTLScanner_ReapsExpiredSessionWithinOneTick(t *testing.T) {
	store, sqlDB, teardown := openConcreteStore(t)
	defer teardown()

	seedClient(t, sqlDB, "ns-a", db.DefaultClient)
	// ttl_minutes=0 → expires_at == last_activity → immediately expired.
	expired := seedSession(t, sqlDB, "ns-a", db.DefaultClient, 0)

	svc := session.New(store, session.Config{})
	scanner := session.NewScanner(svc, session.ScannerConfig{
		Interval: 20 * time.Millisecond,
	}, logging.Nop())

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		_ = scanner.Run(ctx)
		close(done)
	}()
	defer func() {
		cancel()
		<-done
	}()

	deadline := time.Now().Add(500 * time.Millisecond)
	for time.Now().Before(deadline) {
		if countRows(t, sqlDB,
			`SELECT COUNT(*) FROM sessions WHERE id = $1`, expired.ID,
		) == 0 {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("scanner did not delete expired session within 500ms")
}

func TestTTLScanner_CrossNamespaceExpiry(t *testing.T) {
	store, sqlDB, teardown := openConcreteStore(t)
	defer teardown()

	seedClient(t, sqlDB, "ns-a", db.DefaultClient)
	seedClient(t, sqlDB, "ns-b", db.DefaultClient)
	expA := seedSession(t, sqlDB, "ns-a", db.DefaultClient, 0)
	expB := seedSession(t, sqlDB, "ns-b", db.DefaultClient, 0)
	// Fresh sessions (60m TTL) must survive.
	freshA := seedSession(t, sqlDB, "ns-a", db.DefaultClient, 60)
	freshB := seedSession(t, sqlDB, "ns-b", db.DefaultClient, 60)

	svc := session.New(store, session.Config{})
	scanner := session.NewScanner(svc, session.ScannerConfig{
		Interval: 20 * time.Millisecond,
	}, logging.Nop())

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		_ = scanner.Run(ctx)
		close(done)
	}()
	defer func() {
		cancel()
		<-done
	}()

	expiredGone := func() bool {
		return countRows(t, sqlDB,
			`SELECT COUNT(*) FROM sessions WHERE id IN ($1, $2)`, expA.ID, expB.ID,
		) == 0
	}
	deadline := time.Now().Add(500 * time.Millisecond)
	for time.Now().Before(deadline) {
		if expiredGone() {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if !expiredGone() {
		t.Fatal("expired sessions not reaped from both namespaces within 500ms")
	}
	if n := countRows(t, sqlDB,
		`SELECT COUNT(*) FROM sessions WHERE id IN ($1, $2)`, freshA.ID, freshB.ID,
	); n != 2 {
		t.Errorf("fresh sessions count = %d; want 2 (scanner must not touch unexpired rows)", n)
	}
}

func TestTTLScanner_ContextCancelStopsCleanly(t *testing.T) {
	store, _, teardown := openConcreteStore(t)
	defer teardown()

	svc := session.New(store, session.Config{})
	scanner := session.NewScanner(svc, session.ScannerConfig{
		Interval: time.Second, // long enough that we cancel before any tick
	}, logging.Nop())

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		_ = scanner.Run(ctx)
		close(done)
	}()
	cancel()
	select {
	case <-done:
	case <-time.After(200 * time.Millisecond):
		t.Fatal("scanner did not exit within 200ms of context cancel")
	}
}

func TestTTLScanner_QueryUsesIndex(t *testing.T) {
	// Guard against future regressions where someone alters the schema or
	// the query in a way that drops index usage. Forces enable_seqscan=off
	// for the EXPLAIN — at integration-test row counts the planner would
	// reasonably pick a seq scan; this test asserts the index is *available*,
	// not that it's chosen at small scale.
	_, sqlDB, teardown := openConcreteStore(t)
	defer teardown()

	seedClient(t, sqlDB, "ns-a", db.DefaultClient)
	for i := 0; i < 50; i++ {
		seedSession(t, sqlDB, "ns-a", db.DefaultClient, 60)
	}
	if _, err := sqlDB.Exec(`ANALYZE sessions`); err != nil {
		t.Fatalf("ANALYZE: %v", err)
	}

	conn, err := sqlDB.Conn(context.Background())
	if err != nil {
		t.Fatalf("acquire conn: %v", err)
	}
	defer func() { _ = conn.Close() }()
	// SET (without LOCAL) persists for the lifetime of this conn — fine
	// because we Close() it at function end.
	if _, err := conn.ExecContext(context.Background(), `SET enable_seqscan = off`); err != nil {
		t.Fatalf("disable seqscan: %v", err)
	}

	rows, err := conn.QueryContext(context.Background(),
		`EXPLAIN SELECT id, namespace FROM sessions WHERE expires_at < $1`, time.Now())
	if err != nil {
		t.Fatalf("EXPLAIN: %v", err)
	}
	defer func() { _ = rows.Close() }()

	var plan strings.Builder
	for rows.Next() {
		var line string
		if err := rows.Scan(&line); err != nil {
			t.Fatalf("scan plan row: %v", err)
		}
		plan.WriteString(line)
		plan.WriteString("\n")
	}
	if !strings.Contains(plan.String(), "sessions_expires_at_idx") {
		t.Errorf("EXPLAIN plan does not reference sessions_expires_at_idx — index missing or query rewritten so the planner can't use it:\n%s", plan.String())
	}
}
