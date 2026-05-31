// Copyright 2026 The CALM Authors
// SPDX-License-Identifier: Apache-2.0

package session

import (
	"time"

	lru "github.com/hashicorp/golang-lru/v2"
	"golang.org/x/sync/singleflight"
)

type dedupKey struct {
	Namespace string
	Client    string
	Key       string
}

// dedupEntry carries the full committed response shape so a retry returns
// byte-identical fields to the original create — not just the same token.
type dedupEntry struct {
	SessionToken string
	SessionID    int64
	Client       string
	TTLMinutes   int
	CreatedAt    time.Time
	IssuedAt     time.Time
}

// idempotencyStore is per-pod; multi-pod retries may land on different pods
// and create distinct sessions. The embedded singleflight.Group collapses
// concurrent calls with the same key into one mint+INSERT.
type idempotencyStore struct {
	c   *lru.Cache[dedupKey, dedupEntry]
	ttl time.Duration
	now func() time.Time
	sf  singleflight.Group
}

func newIdempotencyStore(size int, ttl time.Duration, now func() time.Time) *idempotencyStore {
	if size <= 0 {
		return nil
	}
	// lru.New only errors on size <= 0, just guarded.
	c, _ := lru.New[dedupKey, dedupEntry](size)
	if now == nil {
		now = time.Now
	}
	return &idempotencyStore{c: c, ttl: ttl, now: now}
}

func (s *idempotencyStore) Do(ns, client, key string, fn func() (dedupEntry, error)) (dedupEntry, error) {
	if s == nil || key == "" {
		return fn()
	}
	// NUL separator is safe — dedupKey fields are non-binary strings.
	sfKey := ns + "\x00" + client + "\x00" + key
	v, err, _ := s.sf.Do(sfKey, func() (any, error) {
		return fn()
	})
	if err != nil {
		return dedupEntry{}, err
	}
	return v.(dedupEntry), nil
}

func (s *idempotencyStore) Resolve(ns, client, key string) (dedupEntry, bool) {
	if s == nil || key == "" {
		return dedupEntry{}, false
	}
	k := dedupKey{Namespace: ns, Client: client, Key: key}
	entry, ok := s.c.Get(k)
	if !ok {
		return dedupEntry{}, false
	}
	if s.now().Sub(entry.IssuedAt) >= s.ttl {
		s.c.Remove(k)
		return dedupEntry{}, false
	}
	return entry, true
}

func (s *idempotencyStore) Store(ns, key string, entry dedupEntry) {
	if s == nil || key == "" {
		return
	}
	entry.IssuedAt = s.now()
	s.c.Add(dedupKey{Namespace: ns, Client: entry.Client, Key: key}, entry)
}
