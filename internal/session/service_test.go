// Copyright 2026 The CALM Authors
// SPDX-License-Identifier: Apache-2.0

//go:build mocks

package session

import (
	"context"
	"errors"
	"testing"
	"time"

	logging "github.com/one-harsh/context-logging"
	"github.com/stretchr/testify/mock"

	"github.com/one-harsh/calm/internal/auth"
	"github.com/one-harsh/calm/internal/db"
)

// serviceHarness wires Service against fresh mock DAL + repos + a real cache
// (cap 100). Cache-state assertions are observational — driven by whether the
// subsequent Lookup hits the mocked SessionRepo.Get.
type serviceHarness struct {
	svc      *Service
	dal      *db.MockDAL
	sessions *db.MockSessionRepo
	clients  *db.MockClientRepo
}

func newServiceHarness(t *testing.T, cacheSize int) *serviceHarness {
	dal := db.NewMockDAL(t)
	sessions := db.NewMockSessionRepo(t)
	clients := db.NewMockClientRepo(t)
	dal.EXPECT().Sessions().Return(sessions).Maybe()
	dal.EXPECT().Clients().Return(clients).Maybe()
	return &serviceHarness{
		svc:      New(dal, Config{CacheSize: cacheSize}, logging.Nop()),
		dal:      dal,
		sessions: sessions,
		clients:  clients,
	}
}

// hashFor centralizes the namespace-scoped hashing the service performs
// before calling the DAL. Mock expectations match on the exact []byte the
// service will produce.
func hashFor(namespace, sessionToken string) []byte {
	return auth.HashToken(namespace, sessionToken)
}

// ---------- New / constructor ----------

func TestNew_NonPositiveCacheSizeUsesNoopCache(t *testing.T) {
	dal := db.NewMockDAL(t)
	svc := New(dal, Config{}, logging.Nop())
	if _, ok := svc.cache.(noopCache); !ok {
		t.Errorf("New(_, Config{}) cache type = %T; want noopCache", svc.cache)
	}
}

// ---------- Create — validation ----------

func TestCreate_EmptyNamespaceRejectedBeforeAnyRepoCall(t *testing.T) {
	h := newServiceHarness(t, 100)
	err := h.svc.Create(context.Background(), &db.Session{TTLMinutes: 60}, "")
	if !errors.Is(err, db.ErrNamespaceRequired) {
		t.Errorf("got %v; want ErrNamespaceRequired", err)
	}
}

func TestCreate_NonPositiveTTLRejectedBeforeAnyRepoCall(t *testing.T) {
	h := newServiceHarness(t, 100)
	for _, ttl := range []int{0, -1, -100} {
		err := h.svc.Create(context.Background(), &db.Session{Namespace: "ns-a", TTLMinutes: ttl}, "")
		if !errors.Is(err, db.ErrInvalidTTL) {
			t.Errorf("TTL=%d: got %v; want ErrInvalidTTL", ttl, err)
		}
	}
}

// ---------- Create — orchestration ----------

func TestCreate_HappyPath_InsertsSessionThenPrimesCache(t *testing.T) {
	h := newServiceHarness(t, 100)
	expectedCreatedAt := time.Date(2026, 5, 26, 12, 0, 0, 0, time.UTC)

	var minted string
	h.sessions.EXPECT().Create(mock.Anything, mock.Anything).
		RunAndReturn(func(_ context.Context, s *db.Session) error {
			// Service must have minted the raw token and populated the hash
			// before reaching the DAL boundary.
			if s.SessionToken == "" {
				t.Errorf("DAL Create saw empty SessionToken; service did not mint")
			}
			if len(s.SessionTokenHash) == 0 {
				t.Errorf("DAL Create saw empty SessionTokenHash; service did not populate")
			}
			minted = s.SessionToken
			// Stand in for the RETURNING flow: DAL populates ID + CreatedAt.
			s.ID = 42
			s.CreatedAt = expectedCreatedAt
			return nil
		}).Once()

	in := &db.Session{Namespace: "ns-a", Client: "alice", TTLMinutes: 60}
	if err := h.svc.Create(context.Background(), in, ""); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if in.ID != 42 {
		t.Errorf("input ID after Create = %d; want propagated from DAL = 42", in.ID)
	}
	if in.SessionToken == "" {
		t.Errorf("input SessionToken after Create is empty; service must surface the raw token")
	}
	if !in.CreatedAt.Equal(expectedCreatedAt) {
		t.Errorf("input CreatedAt = %v; want propagated from DAL = %v", in.CreatedAt, expectedCreatedAt)
	}

	// Cache primed: subsequent Lookup must NOT call SessionRepo.Get.
	md, err := h.svc.Lookup(context.Background(), "ns-a", minted)
	if err != nil {
		t.Fatalf("Lookup after Create: %v", err)
	}
	if md.ID != 42 || md.Client != "alice" || md.TTLMinutes != 60 || !md.CreatedAt.Equal(expectedCreatedAt) {
		t.Errorf("cached metadata = %+v; want id=42 client=alice ttl=60 createdAt=%v", md, expectedCreatedAt)
	}
}

func TestCreate_EmptyClientDefaults(t *testing.T) {
	h := newServiceHarness(t, 100)
	h.sessions.EXPECT().Create(mock.Anything, mock.Anything).Return(nil).Once()

	in := &db.Session{Namespace: "ns-a", TTLMinutes: 60}
	if err := h.svc.Create(context.Background(), in, ""); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if in.Client != db.DefaultClient {
		t.Errorf("input Client after Create = %q; want %q (normalized in place)", in.Client, db.DefaultClient)
	}
}

func TestCreate_ClientNotFound_CacheNotPrimed(t *testing.T) {
	// sessions.Create translates FK violation (23503) on the
	// (namespace, client) → clients FK to ErrClientNotFound. Service.Create
	// just surfaces it; no separate Register step exists.
	h := newServiceHarness(t, 100)
	h.sessions.EXPECT().Create(mock.Anything, mock.Anything).
		RunAndReturn(func(_ context.Context, _ *db.Session) error {
			return db.ErrClientNotFound
		}).Once()

	in := &db.Session{Namespace: "ns-a", Client: "unregistered", TTLMinutes: 60}
	err := h.svc.Create(context.Background(), in, "")
	if !errors.Is(err, db.ErrClientNotFound) {
		t.Fatalf("Create error = %v; want ErrClientNotFound", err)
	}

	// Cache NOT primed: next Lookup must reach SessionRepo.Get.
	// The service had minted a token before the DAL failed; that's the token
	// we'd next try to look up.
	h.sessions.EXPECT().Get(mock.Anything, "ns-a", mock.Anything).Return(db.Session{}, db.ErrSessionNotFound).Once()
	if _, err := h.svc.Lookup(context.Background(), "ns-a", in.SessionToken); !errors.Is(err, db.ErrSessionNotFound) {
		t.Errorf("Lookup after failed Create: got %v; want ErrSessionNotFound (proves cache was not primed)", err)
	}
}

func TestCreate_SessionInsertFails_CacheNotPrimed(t *testing.T) {
	h := newServiceHarness(t, 100)
	insertErr := db.ErrSessionExists
	h.sessions.EXPECT().Create(mock.Anything, mock.Anything).Return(insertErr).Once()

	in := &db.Session{Namespace: "ns-a", Client: "alice", TTLMinutes: 60}
	err := h.svc.Create(context.Background(), in, "")
	if !errors.Is(err, insertErr) {
		t.Fatalf("Create error = %v; want %v", err, insertErr)
	}

	h.sessions.EXPECT().Get(mock.Anything, "ns-a", mock.Anything).Return(db.Session{}, db.ErrSessionNotFound).Once()
	if _, err := h.svc.Lookup(context.Background(), "ns-a", in.SessionToken); !errors.Is(err, db.ErrSessionNotFound) {
		t.Errorf("Lookup after failed session insert: got %v; want ErrSessionNotFound (proves cache was not primed)", err)
	}
}

// ---------- Lookup ----------

func TestLookup_CacheHitDoesNotCallDAL(t *testing.T) {
	h := newServiceHarness(t, 100)
	// Prime via real cache.
	h.svc.cache.Put(cacheKey{Namespace: "ns-a", SessionToken: "s1"},
		SessionMetadata{ID: 7, Client: "alice", TTLMinutes: 60, CreatedAt: time.Now().UTC()})

	// No SessionRepo.Get expectation → asserts via mock cleanup that it wasn't called.
	md, err := h.svc.Lookup(context.Background(), "ns-a", "s1")
	if err != nil {
		t.Fatalf("Lookup: %v", err)
	}
	if md.Client != "alice" || md.ID != 7 {
		t.Errorf("md = %+v; want client=alice id=7", md)
	}
}

func TestLookup_CacheMiss_DBHit_PopulatesCache(t *testing.T) {
	h := newServiceHarness(t, 100)
	created := time.Date(2026, 5, 26, 12, 0, 0, 0, time.UTC)
	h.sessions.EXPECT().Get(mock.Anything, "ns-a", hashFor("ns-a", "s1")).Return(
		db.Session{ID: 11, Namespace: "ns-a", Client: "alice", TTLMinutes: 60, CreatedAt: created},
		nil,
	).Once()

	md, err := h.svc.Lookup(context.Background(), "ns-a", "s1")
	if err != nil {
		t.Fatalf("first Lookup: %v", err)
	}
	if md.ID != 11 || md.Client != "alice" || md.TTLMinutes != 60 || !md.CreatedAt.Equal(created) {
		t.Errorf("first lookup md = %+v", md)
	}

	// Second Lookup must serve from cache — no second Get expectation.
	md2, err := h.svc.Lookup(context.Background(), "ns-a", "s1")
	if err != nil {
		t.Fatalf("second Lookup: %v", err)
	}
	if md2 != md {
		t.Errorf("cached md2 = %+v; want %+v", md2, md)
	}
}

func TestLookup_CacheMiss_DBNotFound_DoesNotPopulateCache(t *testing.T) {
	h := newServiceHarness(t, 100)
	// Two calls expected — the second proves the first didn't poison the cache
	// with a negative entry. (Negative caching is explicitly rejected by HLD.)
	h.sessions.EXPECT().Get(mock.Anything, "ns-a", hashFor("ns-a", "s1")).Return(db.Session{}, db.ErrSessionNotFound).Twice()

	if _, err := h.svc.Lookup(context.Background(), "ns-a", "s1"); !errors.Is(err, db.ErrSessionNotFound) {
		t.Fatalf("first Lookup: got %v; want ErrSessionNotFound", err)
	}
	if _, err := h.svc.Lookup(context.Background(), "ns-a", "s1"); !errors.Is(err, db.ErrSessionNotFound) {
		t.Errorf("second Lookup (post-miss): got %v; want ErrSessionNotFound (proves no negative caching)", err)
	}
}

func TestLookup_CacheMiss_DBError_Propagates(t *testing.T) {
	h := newServiceHarness(t, 100)
	storageErr := errors.New("simulated storage backend failure")
	h.sessions.EXPECT().Get(mock.Anything, "ns-a", hashFor("ns-a", "s1")).Return(db.Session{}, storageErr).Once()

	if _, err := h.svc.Lookup(context.Background(), "ns-a", "s1"); !errors.Is(err, storageErr) {
		t.Errorf("got %v; want wrapping %v", err, storageErr)
	}
}

// ---------- Get (proxy) ----------

func TestGet_ProxiesToDAL(t *testing.T) {
	h := newServiceHarness(t, 100)
	want := db.Session{ID: 5, Namespace: "ns-a", Client: "alice", TTLMinutes: 60}
	h.sessions.EXPECT().Get(mock.Anything, "ns-a", hashFor("ns-a", "s1")).Return(want, nil).Once()

	got, err := h.svc.Get(context.Background(), "ns-a", "s1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.ID != want.ID || got.Namespace != want.Namespace || got.Client != want.Client || got.TTLMinutes != want.TTLMinutes {
		t.Errorf("got %+v; want %+v", got, want)
	}
}

func TestGet_DoesNotConsultOrPopulateCache(t *testing.T) {
	h := newServiceHarness(t, 100)
	// Prime cache: subsequent Get must still call DAL (cold-path bypass).
	h.svc.cache.Put(cacheKey{Namespace: "ns-a", SessionToken: "s1"}, SessionMetadata{Client: "cached"})
	h.sessions.EXPECT().Get(mock.Anything, "ns-a", hashFor("ns-a", "s1")).Return(
		db.Session{ID: 5, Namespace: "ns-a", Client: "from-db", TTLMinutes: 60}, nil,
	).Once()

	got, err := h.svc.Get(context.Background(), "ns-a", "s1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Client != "from-db" {
		t.Errorf("Get returned cached client %q; want from-db (Get must bypass cache for the labels-bearing read)", got.Client)
	}
}

// ---------- Touch ----------

func TestTouch_SuccessLeavesCacheIntact(t *testing.T) {
	h := newServiceHarness(t, 100)
	h.svc.cache.Put(cacheKey{Namespace: "ns-a", SessionToken: "s1"}, SessionMetadata{Client: "alice", TTLMinutes: 60})
	h.sessions.EXPECT().Touch(mock.Anything, "ns-a", hashFor("ns-a", "s1"), mock.Anything).Return(nil).Once()

	if err := h.svc.Touch(context.Background(), "ns-a", "s1", time.Now()); err != nil {
		t.Fatalf("Touch: %v", err)
	}
	// Cache must still hit — no SessionRepo.Get expectation.
	if md, err := h.svc.Lookup(context.Background(), "ns-a", "s1"); err != nil || md.Client != "alice" {
		t.Errorf("Lookup after Touch: md=%+v err=%v; want cached hit", md, err)
	}
}

func TestTouch_NotFoundEvictsStaleEntry(t *testing.T) {
	h := newServiceHarness(t, 100)
	h.svc.cache.Put(cacheKey{Namespace: "ns-a", SessionToken: "s1"}, SessionMetadata{Client: "alice", TTLMinutes: 60})
	h.sessions.EXPECT().Touch(mock.Anything, "ns-a", hashFor("ns-a", "s1"), mock.Anything).Return(db.ErrSessionNotFound).Once()

	if err := h.svc.Touch(context.Background(), "ns-a", "s1", time.Now()); !errors.Is(err, db.ErrSessionNotFound) {
		t.Fatalf("Touch: %v; want ErrSessionNotFound", err)
	}
	// Cache evicted: next Lookup must reach DAL.
	h.sessions.EXPECT().Get(mock.Anything, "ns-a", hashFor("ns-a", "s1")).Return(db.Session{}, db.ErrSessionNotFound).Once()
	if _, err := h.svc.Lookup(context.Background(), "ns-a", "s1"); !errors.Is(err, db.ErrSessionNotFound) {
		t.Errorf("Lookup after Touch not-found: got %v; want ErrSessionNotFound", err)
	}
}

func TestTouch_TransientErrorDoesNotEvict(t *testing.T) {
	h := newServiceHarness(t, 100)
	cached := SessionMetadata{Client: "alice", TTLMinutes: 60}
	h.svc.cache.Put(cacheKey{Namespace: "ns-a", SessionToken: "s1"}, cached)
	transient := errors.New("simulated transient storage failure")
	h.sessions.EXPECT().Touch(mock.Anything, "ns-a", hashFor("ns-a", "s1"), mock.Anything).Return(transient).Once()

	if err := h.svc.Touch(context.Background(), "ns-a", "s1", time.Now()); !errors.Is(err, transient) {
		t.Fatalf("Touch: %v; want %v", err, transient)
	}
	// Cache intact — sentinel gate matters: only ErrSessionNotFound evicts.
	if md, err := h.svc.Lookup(context.Background(), "ns-a", "s1"); err != nil || md != cached {
		t.Errorf("Lookup after transient Touch: md=%+v err=%v; want intact cached hit", md, err)
	}
}

// ---------- Delete ----------

func TestDelete_SuccessEvictsCache(t *testing.T) {
	h := newServiceHarness(t, 100)
	h.svc.cache.Put(cacheKey{Namespace: "ns-a", SessionToken: "s1"}, SessionMetadata{Client: "alice"})
	h.sessions.EXPECT().Delete(mock.Anything, "ns-a", hashFor("ns-a", "s1")).Return(
		db.DeleteSessionResult{ID: 9, Cascaded: db.CascadeCounts{Events: 7}}, nil,
	).Once()

	res, err := h.svc.Delete(context.Background(), "ns-a", "s1")
	if err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if res.ID != 9 || res.Cascaded.Events != 7 {
		t.Errorf("result = %+v; want ID=9 Cascaded.Events=7", res)
	}
	h.sessions.EXPECT().Get(mock.Anything, "ns-a", hashFor("ns-a", "s1")).Return(db.Session{}, db.ErrSessionNotFound).Once()
	if _, err := h.svc.Lookup(context.Background(), "ns-a", "s1"); !errors.Is(err, db.ErrSessionNotFound) {
		t.Errorf("Lookup after Delete: got %v; want ErrSessionNotFound", err)
	}
}

func TestDelete_NotFoundStillEvictsCache(t *testing.T) {
	h := newServiceHarness(t, 100)
	h.svc.cache.Put(cacheKey{Namespace: "ns-a", SessionToken: "s1"}, SessionMetadata{Client: "alice"})
	h.sessions.EXPECT().Delete(mock.Anything, "ns-a", hashFor("ns-a", "s1")).Return(
		db.DeleteSessionResult{}, db.ErrSessionNotFound,
	).Once()

	if _, err := h.svc.Delete(context.Background(), "ns-a", "s1"); !errors.Is(err, db.ErrSessionNotFound) {
		t.Fatalf("Delete: %v; want ErrSessionNotFound", err)
	}
	h.sessions.EXPECT().Get(mock.Anything, "ns-a", hashFor("ns-a", "s1")).Return(db.Session{}, db.ErrSessionNotFound).Once()
	if _, err := h.svc.Lookup(context.Background(), "ns-a", "s1"); !errors.Is(err, db.ErrSessionNotFound) {
		t.Errorf("Lookup after Delete: got %v; want ErrSessionNotFound (proves not-found path still invalidates)", err)
	}
}

func TestDelete_TransientErrorDoesNotEvict(t *testing.T) {
	h := newServiceHarness(t, 100)
	cached := SessionMetadata{Client: "alice", TTLMinutes: 60}
	h.svc.cache.Put(cacheKey{Namespace: "ns-a", SessionToken: "s1"}, cached)
	transient := errors.New("simulated transient storage failure")
	h.sessions.EXPECT().Delete(mock.Anything, "ns-a", hashFor("ns-a", "s1")).Return(db.DeleteSessionResult{}, transient).Once()

	if _, err := h.svc.Delete(context.Background(), "ns-a", "s1"); !errors.Is(err, transient) {
		t.Fatalf("Delete: %v; want %v", err, transient)
	}
	if md, err := h.svc.Lookup(context.Background(), "ns-a", "s1"); err != nil || md != cached {
		t.Errorf("Lookup after transient Delete: md=%+v err=%v; want intact cached hit", md, err)
	}
}

// ---------- DeleteByID (scanner path) ----------

func TestDeleteByID_InvalidatesNamespace(t *testing.T) {
	h := newServiceHarness(t, 100)
	// Two ns-a entries + an ns-b entry that must survive.
	h.svc.cache.Put(cacheKey{Namespace: "ns-a", SessionToken: "tok-1"}, SessionMetadata{Client: "a1"})
	h.svc.cache.Put(cacheKey{Namespace: "ns-a", SessionToken: "tok-2"}, SessionMetadata{Client: "a2"})
	keepKey := cacheKey{Namespace: "ns-b", SessionToken: "tok-1"}
	keepMD := SessionMetadata{Client: "b1"}
	h.svc.cache.Put(keepKey, keepMD)

	h.sessions.EXPECT().DeleteByID(mock.Anything, "ns-a", int64(42)).Return(
		db.DeleteSessionResult{ID: 42}, nil,
	).Once()

	if _, err := h.svc.DeleteByID(context.Background(), "ns-a", 42); err != nil {
		t.Fatalf("DeleteByID: %v", err)
	}
	// ns-a entries gone; ns-b survives.
	h.sessions.EXPECT().Get(mock.Anything, "ns-a", mock.Anything).Return(db.Session{}, db.ErrSessionNotFound).Once()
	if _, err := h.svc.Lookup(context.Background(), "ns-a", "tok-1"); !errors.Is(err, db.ErrSessionNotFound) {
		t.Errorf("ns-a/tok-1 Lookup: got %v; want ErrSessionNotFound", err)
	}
	if md, err := h.svc.Lookup(context.Background(), "ns-b", "tok-1"); err != nil || md != keepMD {
		t.Errorf("ns-b/tok-1 Lookup: md=%+v err=%v; want intact (over-eviction regression)", md, err)
	}
}

// ---------- List / Count / ScanExpired (proxies) ----------

func TestList_ProxiesToDAL(t *testing.T) {
	h := newServiceHarness(t, 100)
	want := []db.ManagedSession{{ID: 1, Namespace: "ns-a"}, {ID: 2, Namespace: "ns-a"}}
	filter := db.ListSessionsFilter{Namespace: "ns-a"}
	h.sessions.EXPECT().List(mock.Anything, filter).Return(want, nil).Once()

	got, err := h.svc.List(context.Background(), filter)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(got) != 2 || got[0].ID != 1 || got[1].ID != 2 {
		t.Errorf("got %+v; want %+v", got, want)
	}
}

func TestCount_ProxiesToDAL(t *testing.T) {
	h := newServiceHarness(t, 100)
	filter := db.ListSessionsFilter{Namespace: "ns-a"}
	h.sessions.EXPECT().Count(mock.Anything, filter).Return(42, nil).Once()

	got, err := h.svc.Count(context.Background(), filter)
	if err != nil {
		t.Fatalf("Count: %v", err)
	}
	if got != 42 {
		t.Errorf("got %d; want 42", got)
	}
}

func TestScanExpired_ProxiesToDAL(t *testing.T) {
	h := newServiceHarness(t, 100)
	now := time.Now().UTC()
	want := []db.SessionRef{{ID: 1, Namespace: "ns-a"}}
	h.sessions.EXPECT().ScanExpired(mock.Anything, now).Return(want, nil).Once()

	got, err := h.svc.ScanExpired(context.Background(), now)
	if err != nil {
		t.Fatalf("ScanExpired: %v", err)
	}
	if len(got) != 1 || got[0] != want[0] {
		t.Errorf("got %+v; want %+v", got, want)
	}
}

// ---------- DeleteAll ----------

func TestDeleteAll_NamespacedSuccessInvalidatesNamespaceOnly(t *testing.T) {
	h := newServiceHarness(t, 100)
	// Seed entries across two namespaces.
	h.svc.cache.Put(cacheKey{Namespace: "ns-a", SessionToken: "t1"}, SessionMetadata{Client: "a1"})
	h.svc.cache.Put(cacheKey{Namespace: "ns-a", SessionToken: "t2"}, SessionMetadata{Client: "a2"})
	keepKey := cacheKey{Namespace: "ns-b", SessionToken: "t1"}
	keepMD := SessionMetadata{Client: "b1"}
	h.svc.cache.Put(keepKey, keepMD)

	filter := db.ListSessionsFilter{Namespace: "ns-a"}
	h.sessions.EXPECT().DeleteAll(mock.Anything, filter).Return(
		db.DeleteSessionsResult{DeletedSessions: 2}, nil,
	).Once()

	res, err := h.svc.DeleteAll(context.Background(), filter)
	if err != nil {
		t.Fatalf("DeleteAll: %v", err)
	}
	if res.DeletedSessions != 2 {
		t.Errorf("DeletedSessions = %d; want 2", res.DeletedSessions)
	}

	// ns-a entries evicted; ns-b survives.
	h.sessions.EXPECT().Get(mock.Anything, "ns-a", mock.Anything).Return(db.Session{}, db.ErrSessionNotFound).Once()
	if _, err := h.svc.Lookup(context.Background(), "ns-a", "t1"); !errors.Is(err, db.ErrSessionNotFound) {
		t.Errorf("ns-a/t1 Lookup: got %v; want ErrSessionNotFound", err)
	}
	if md, err := h.svc.Lookup(context.Background(), "ns-b", "t1"); err != nil || md != keepMD {
		t.Errorf("ns-b/t1 Lookup: md=%+v err=%v; want intact (over-eviction regression)", md, err)
	}
}

func TestDeleteAll_EmptyNamespaceSuccessPurgesEntireCache(t *testing.T) {
	h := newServiceHarness(t, 100)
	h.svc.cache.Put(cacheKey{Namespace: "ns-a", SessionToken: "t1"}, SessionMetadata{Client: "a1"})
	h.svc.cache.Put(cacheKey{Namespace: "ns-b", SessionToken: "t1"}, SessionMetadata{Client: "b1"})

	filter := db.ListSessionsFilter{Namespace: ""}
	h.sessions.EXPECT().DeleteAll(mock.Anything, filter).Return(
		db.DeleteSessionsResult{DeletedSessions: 2}, nil,
	).Once()

	if _, err := h.svc.DeleteAll(context.Background(), filter); err != nil {
		t.Fatalf("DeleteAll: %v", err)
	}
	// Both namespaces' entries gone — both Lookups must reach DAL.
	h.sessions.EXPECT().Get(mock.Anything, "ns-a", mock.Anything).Return(db.Session{}, db.ErrSessionNotFound).Once()
	h.sessions.EXPECT().Get(mock.Anything, "ns-b", mock.Anything).Return(db.Session{}, db.ErrSessionNotFound).Once()
	if _, err := h.svc.Lookup(context.Background(), "ns-a", "t1"); !errors.Is(err, db.ErrSessionNotFound) {
		t.Errorf("ns-a/t1 Lookup after Purge: got %v", err)
	}
	if _, err := h.svc.Lookup(context.Background(), "ns-b", "t1"); !errors.Is(err, db.ErrSessionNotFound) {
		t.Errorf("ns-b/t1 Lookup after Purge: got %v", err)
	}
}

func TestDeleteAll_DALErrorDoesNotInvalidate(t *testing.T) {
	h := newServiceHarness(t, 100)
	cached := SessionMetadata{Client: "alice", TTLMinutes: 60}
	h.svc.cache.Put(cacheKey{Namespace: "ns-a", SessionToken: "t1"}, cached)
	storageErr := errors.New("simulated bulk delete failure")
	filter := db.ListSessionsFilter{Namespace: "ns-a"}
	h.sessions.EXPECT().DeleteAll(mock.Anything, filter).Return(db.DeleteSessionsResult{}, storageErr).Once()

	if _, err := h.svc.DeleteAll(context.Background(), filter); !errors.Is(err, storageErr) {
		t.Fatalf("DeleteAll: %v; want %v", err, storageErr)
	}
	if md, err := h.svc.Lookup(context.Background(), "ns-a", "t1"); err != nil || md != cached {
		t.Errorf("Lookup after failed DeleteAll: md=%+v err=%v; want intact cached hit", md, err)
	}
}

// ---------- Idempotency: post-TTL boundary ----------

// TestCreate_ConcurrentRetriesAfterTTLExpiryProduceOneSession proves that a
// retry storm arriving JUST after the dedup TTL expires still collapses to a
// single INSERT. Without singleflight the failure shape is: N goroutines all
// observe the expired entry, all see Resolve return (zero, false), all proceed
// to mint+INSERT, and N session rows are leaked. The singleflight wrap in
// Service.Create's Do() closure is what keeps this safe.
func TestCreate_ConcurrentRetriesAfterTTLExpiryProduceOneSession(t *testing.T) {
	dal := db.NewMockDAL(t)
	sessions := db.NewMockSessionRepo(t)
	clients := db.NewMockClientRepo(t)
	dal.EXPECT().Sessions().Return(sessions).Maybe()
	dal.EXPECT().Clients().Return(clients).Maybe()

	// Tiny TTL so the test can cross the boundary without an interactive
	// wait. 50ms TTL + 100ms sleep is generous against scheduler jitter.
	svc := New(dal, Config{
		CacheSize:          100,
		IdempotencyKeyTTL:  50 * time.Millisecond,
		IdempotencyKeySize: 100,
	}, logging.Nop())

	const key = "key-X"
	const namespace = "ns-a"
	const client = "alice"

	// First Create populates the dedup entry.
	sessions.EXPECT().Create(mock.Anything, mock.Anything).
		RunAndReturn(func(_ context.Context, s *db.Session) error {
			s.ID = 1
			s.CreatedAt = time.Now().UTC()
			return nil
		}).Once()

	first := &db.Session{Namespace: namespace, Client: client, TTLMinutes: 60}
	if err := svc.Create(context.Background(), first, key); err != nil {
		t.Fatalf("first Create: %v", err)
	}
	originalToken := first.SessionToken

	// Cross the dedup TTL boundary. From here on, Resolve must miss.
	time.Sleep(100 * time.Millisecond)

	// The critical assertion: across N concurrent retries with the SAME
	// expired key, only ONE DAL.Create runs. mockery's Once() fails the test
	// loudly if Create is invoked more than once.
	sessions.EXPECT().Create(mock.Anything, mock.Anything).
		RunAndReturn(func(_ context.Context, s *db.Session) error {
			s.ID = 2
			s.CreatedAt = time.Now().UTC()
			return nil
		}).Once()

	const N = 20
	type result struct{ token string }
	results := make(chan result, N)
	start := make(chan struct{})
	for i := 0; i < N; i++ {
		go func() {
			<-start
			sess := &db.Session{Namespace: namespace, Client: client, TTLMinutes: 60}
			if err := svc.Create(context.Background(), sess, key); err != nil {
				t.Errorf("retry Create: %v", err)
				results <- result{}
				return
			}
			results <- result{token: sess.SessionToken}
		}()
	}
	close(start)

	tokens := make(map[string]int, N)
	for i := 0; i < N; i++ {
		r := <-results
		tokens[r.token]++
	}

	if len(tokens) != 1 {
		t.Fatalf("post-TTL concurrent retries returned %d distinct tokens; want 1 (singleflight failed at the boundary): %v",
			len(tokens), tokens)
	}
	for tok := range tokens {
		if tok == originalToken {
			t.Errorf("retry returned the original pre-TTL token; expected a fresh post-TTL mint")
		}
		if tok == "" {
			t.Errorf("retry returned an empty token")
		}
	}
}
