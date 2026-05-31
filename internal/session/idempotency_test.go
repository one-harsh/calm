// Copyright 2026 The CALM Authors
// SPDX-License-Identifier: Apache-2.0

package session

import (
	"sync"
	"testing"
	"time"
)

// fakeClock returns the value at *now. Tests advance time by writing to *now
// without sleeping.
func fakeClock(now *time.Time) func() time.Time {
	return func() time.Time { return *now }
}

func testEntry(token string, id int64) dedupEntry {
	return dedupEntry{
		SessionToken: token,
		SessionID:    id,
		Client:       "alice",
		TTLMinutes:   60,
		CreatedAt:    time.Date(2026, 5, 31, 12, 0, 0, 0, time.UTC),
	}
}

func TestIdempotencyStore_HitMissAndScope(t *testing.T) {
	now := time.Date(2026, 5, 31, 12, 0, 0, 0, time.UTC)
	s := newIdempotencyStore(100, time.Hour, fakeClock(&now))

	// Miss before Store.
	if _, ok := s.Resolve("ns-a", "alice", "key-1"); ok {
		t.Fatal("Resolve before Store returned hit")
	}

	s.Store("ns-a", "key-1", testEntry("TOKEN_A", 1))

	got, ok := s.Resolve("ns-a", "alice", "key-1")
	if !ok || got.SessionToken != "TOKEN_A" || got.SessionID != 1 {
		t.Errorf("hit after Store: ok=%v entry=%+v; want TOKEN_A/1", ok, got)
	}
	if got.Client != "alice" || got.TTLMinutes != 60 || got.CreatedAt.IsZero() {
		t.Errorf("hit entry missing full response shape: %+v", got)
	}

	// Same key, different client → distinct entry.
	if _, ok := s.Resolve("ns-a", "bob", "key-1"); ok {
		t.Errorf("Resolve scoped by client: bob saw alice's entry")
	}
	// Same key, different namespace → distinct entry.
	if _, ok := s.Resolve("ns-b", "alice", "key-1"); ok {
		t.Errorf("Resolve scoped by namespace: ns-b saw ns-a's entry")
	}
}

func TestIdempotencyStore_TTLExpiry(t *testing.T) {
	now := time.Date(2026, 5, 31, 12, 0, 0, 0, time.UTC)
	s := newIdempotencyStore(100, time.Hour, fakeClock(&now))

	s.Store("ns-a", "key-1", testEntry("TOKEN_A", 1))

	// Still inside the window.
	now = now.Add(59 * time.Minute)
	if _, ok := s.Resolve("ns-a", "alice", "key-1"); !ok {
		t.Fatal("Resolve at T+59m: want hit (TTL=1h)")
	}

	// Past the window.
	now = now.Add(2 * time.Minute)
	if _, ok := s.Resolve("ns-a", "alice", "key-1"); ok {
		t.Errorf("Resolve at T+61m: want miss (entry expired)")
	}
}

func TestIdempotencyStore_EmptyKeyIsNoOp(t *testing.T) {
	now := time.Date(2026, 5, 31, 12, 0, 0, 0, time.UTC)
	s := newIdempotencyStore(100, time.Hour, fakeClock(&now))

	s.Store("ns-a", "", testEntry("TOKEN_A", 1))
	if _, ok := s.Resolve("ns-a", "alice", ""); ok {
		t.Errorf("empty key should never resolve (workload didn't opt into dedup)")
	}
}

func TestIdempotencyStore_NilStoreIsSafe(t *testing.T) {
	// size <= 0 returns nil; service treats that as dedup-disabled. Both
	// methods must be nil-safe so callers don't need to nil-check.
	var s *idempotencyStore
	s.Store("ns-a", "key-1", testEntry("TOKEN_A", 1))
	if _, ok := s.Resolve("ns-a", "alice", "key-1"); ok {
		t.Errorf("nil store should never resolve")
	}
}

func TestIdempotencyStore_LRUCapEvictsOldest(t *testing.T) {
	now := time.Date(2026, 5, 31, 12, 0, 0, 0, time.UTC)
	s := newIdempotencyStore(3, time.Hour, fakeClock(&now))

	s.Store("ns-a", "k1", testEntry("T1", 1))
	s.Store("ns-a", "k2", testEntry("T2", 2))
	s.Store("ns-a", "k3", testEntry("T3", 3))
	s.Store("ns-a", "k4", testEntry("T4", 4)) // evicts k1

	if _, ok := s.Resolve("ns-a", "alice", "k1"); ok {
		t.Errorf("k1 should have been evicted at cap")
	}
	for _, k := range []string{"k2", "k3", "k4"} {
		if _, ok := s.Resolve("ns-a", "alice", k); !ok {
			t.Errorf("%s evicted; expected to survive", k)
		}
	}
}

func TestNewIdempotencyStore_NonPositiveSizeReturnsNil(t *testing.T) {
	for _, size := range []int{0, -1, -100} {
		if s := newIdempotencyStore(size, time.Hour, nil); s != nil {
			t.Errorf("newIdempotencyStore(%d, _, _) = non-nil; want nil (dedup disabled)", size)
		}
	}
}

func TestIdempotencyStore_ConcurrentResolveAndStore(t *testing.T) {
	// Smoke test: the underlying hashicorp/golang-lru/v2 is documented
	// thread-safe. Concurrent Resolve+Store across many keys must not panic
	// or trip the race detector.
	now := time.Date(2026, 5, 31, 12, 0, 0, 0, time.UTC)
	s := newIdempotencyStore(1000, time.Hour, fakeClock(&now))

	var wg sync.WaitGroup
	const workers = 8
	const ops = 200
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for i := 0; i < ops; i++ {
				key := time.Now().Format(time.RFC3339Nano) // unique-enough
				s.Store("ns", key, testEntry("T", int64(i)))
				_, _ = s.Resolve("ns", "alice", key)
			}
		}(w)
	}
	wg.Wait()
}
