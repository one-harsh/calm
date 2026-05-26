// Copyright 2026 The CALM Authors
// SPDX-License-Identifier: Apache-2.0

package session

import (
	"testing"
	"time"
)

func TestLRUCache_HitMiss(t *testing.T) {
	c := newCache(100)
	k := cacheKey{Namespace: "ns-a", SessionID: "s1"}
	if _, ok := c.Lookup(k); ok {
		t.Fatal("Lookup on empty cache returned hit")
	}
	v := SessionMetadata{Client: "alice", TTLMinutes: 60, CreatedAt: time.Now().UTC()}
	c.Put(k, v)
	got, ok := c.Lookup(k)
	if !ok {
		t.Fatal("Lookup after Put returned miss")
	}
	if got != v {
		t.Errorf("Lookup: got %+v; want %+v", got, v)
	}
}

func TestLRUCache_InvalidateRemovesEntry(t *testing.T) {
	c := newCache(100)
	k := cacheKey{Namespace: "ns-a", SessionID: "s1"}
	c.Put(k, SessionMetadata{Client: "alice", TTLMinutes: 60})
	c.Invalidate(k)
	if _, ok := c.Lookup(k); ok {
		t.Error("Lookup after Invalidate returned hit")
	}
}

func TestLRUCache_InvalidateNamespace_LeavesOtherNamespaces(t *testing.T) {
	c := newCache(100)
	keep := cacheKey{Namespace: "ns-b", SessionID: "s1"}
	c.Put(cacheKey{Namespace: "ns-a", SessionID: "s1"}, SessionMetadata{Client: "x"})
	c.Put(cacheKey{Namespace: "ns-a", SessionID: "s2"}, SessionMetadata{Client: "y"})
	c.Put(keep, SessionMetadata{Client: "z"})

	c.InvalidateNamespace("ns-a")

	if _, ok := c.Lookup(cacheKey{Namespace: "ns-a", SessionID: "s1"}); ok {
		t.Error("ns-a/s1 still present after InvalidateNamespace")
	}
	if _, ok := c.Lookup(cacheKey{Namespace: "ns-a", SessionID: "s2"}); ok {
		t.Error("ns-a/s2 still present after InvalidateNamespace")
	}
	if _, ok := c.Lookup(keep); !ok {
		t.Error("ns-b/s1 evicted by InvalidateNamespace(\"ns-a\") — over-eviction bug")
	}
}

// Regression: same session_id legitimately exists across namespaces; cache
// entries must not alias.
func TestLRUCache_CrossNamespaceSameSessionIDIsolated(t *testing.T) {
	c := newCache(100)
	a := cacheKey{Namespace: "ns-a", SessionID: "shared"}
	b := cacheKey{Namespace: "ns-b", SessionID: "shared"}
	c.Put(a, SessionMetadata{Client: "alice"})
	c.Put(b, SessionMetadata{Client: "bob"})

	gotA, _ := c.Lookup(a)
	gotB, _ := c.Lookup(b)
	if gotA.Client != "alice" {
		t.Errorf("ns-a lookup: got client %q; want alice", gotA.Client)
	}
	if gotB.Client != "bob" {
		t.Errorf("ns-b lookup: got client %q; want bob", gotB.Client)
	}
}

func TestLRUCache_SizeCapEvictsOldest(t *testing.T) {
	c := newCache(3)
	for i, id := range []string{"s1", "s2", "s3"} {
		c.Put(cacheKey{Namespace: "ns-a", SessionID: id}, SessionMetadata{TTLMinutes: i})
	}
	c.Put(cacheKey{Namespace: "ns-a", SessionID: "s4"}, SessionMetadata{TTLMinutes: 4})

	if _, ok := c.Lookup(cacheKey{Namespace: "ns-a", SessionID: "s1"}); ok {
		t.Error("s1 (oldest) should have been evicted at cap")
	}
	for _, id := range []string{"s2", "s3", "s4"} {
		if _, ok := c.Lookup(cacheKey{Namespace: "ns-a", SessionID: id}); !ok {
			t.Errorf("%s evicted; expected to survive", id)
		}
	}
}

func TestLRUCache_PurgeEmpties(t *testing.T) {
	c := newCache(100)
	c.Put(cacheKey{Namespace: "ns-a", SessionID: "s1"}, SessionMetadata{})
	c.Put(cacheKey{Namespace: "ns-b", SessionID: "s1"}, SessionMetadata{})
	c.Purge()
	if _, ok := c.Lookup(cacheKey{Namespace: "ns-a", SessionID: "s1"}); ok {
		t.Error("ns-a/s1 survived Purge")
	}
	if _, ok := c.Lookup(cacheKey{Namespace: "ns-b", SessionID: "s1"}); ok {
		t.Error("ns-b/s1 survived Purge")
	}
}

func TestNoopCache_AlwaysMisses(t *testing.T) {
	c := noopCache{}
	k := cacheKey{Namespace: "ns-a", SessionID: "s1"}
	c.Put(k, SessionMetadata{Client: "alice"})
	if _, ok := c.Lookup(k); ok {
		t.Error("noopCache.Lookup returned hit; want always-miss")
	}
	c.Invalidate(k)
	c.InvalidateNamespace("ns-a")
	c.Purge()
}

func TestNewCache_NonPositiveSizeReturnsNoop(t *testing.T) {
	for _, size := range []int{0, -1, -100} {
		c := newCache(size)
		k := cacheKey{Namespace: "ns-a", SessionID: "s1"}
		c.Put(k, SessionMetadata{Client: "alice"})
		if _, ok := c.Lookup(k); ok {
			t.Errorf("newCache(%d) returned a working cache; want noop", size)
		}
	}
}
