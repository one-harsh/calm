// Copyright 2026 The CALM Authors
// SPDX-License-Identifier: Apache-2.0

package session

import (
	"time"

	lru "github.com/hashicorp/golang-lru/v2"
)

// SessionMetadata excludes Labels (cold-path, Service.Get) and LastActivity
// (changes on every Touch, would force a cache write per request).
type SessionMetadata struct {
	Client     string
	TTLMinutes int
	CreatedAt  time.Time
}

type cacheKey struct {
	Namespace string
	SessionID string
}

type cache interface {
	Lookup(cacheKey) (SessionMetadata, bool)
	Put(cacheKey, SessionMetadata)
	Invalidate(cacheKey)
	InvalidateNamespace(namespace string)
	Purge()
}

type lruCache struct {
	c *lru.Cache[cacheKey, SessionMetadata]
}

func (l *lruCache) Lookup(k cacheKey) (SessionMetadata, bool) { return l.c.Get(k) }
func (l *lruCache) Put(k cacheKey, v SessionMetadata)         { l.c.Add(k, v) }
func (l *lruCache) Invalidate(k cacheKey)                     { l.c.Remove(k) }
func (l *lruCache) Purge()                                    { l.c.Purge() }

func (l *lruCache) InvalidateNamespace(namespace string) {
	for _, k := range l.c.Keys() {
		if k.Namespace == namespace {
			l.c.Remove(k)
		}
	}
}

type noopCache struct{}

func (noopCache) Lookup(cacheKey) (SessionMetadata, bool) { return SessionMetadata{}, false }
func (noopCache) Put(cacheKey, SessionMetadata)           {}
func (noopCache) Invalidate(cacheKey)                     {}
func (noopCache) InvalidateNamespace(string)              {}
func (noopCache) Purge()                                  {}

// newCache returns noopCache when size <= 0 (cache disabled — every Lookup
// hits DB).
func newCache(size int) cache {
	if size <= 0 {
		return noopCache{}
	}
	c, err := lru.New[cacheKey, SessionMetadata](size)
	if err != nil {
		return noopCache{}
	}
	return &lruCache{c: c}
}
