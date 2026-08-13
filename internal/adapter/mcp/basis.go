// Copyright 2026 The CALM Authors
// SPDX-License-Identifier: Apache-2.0

package mcp

import (
	"crypto/sha256"
	"encoding/hex"
	"path/filepath"
	"sync"
)

type basisRegistry struct {
	mu      sync.Mutex
	entries map[string]basisEntry // fused source label → what it captured
}

type basisEntry struct {
	path   string // resolved target path, cleaned
	hash   string // sha256 of the captured content
	unread bool   // minted by a rejection; content not yet read back
}

func newBasisRegistry() *basisRegistry {
	return &basisRegistry{entries: make(map[string]basisEntry)}
}

func (b *basisRegistry) Record(label, path, content string) {
	b.record(label, path, content, false)
}

func (b *basisRegistry) RecordUnread(label, path, content string) {
	b.record(label, path, content, true)
}

func (b *basisRegistry) record(label, path, content string, unread bool) {
	if label == "" {
		return
	}
	b.mu.Lock()
	b.entries[label] = basisEntry{path: filepath.Clean(path), hash: contentHash(content), unread: unread}
	b.mu.Unlock()
}

func (b *basisRegistry) Unread(label string) bool {
	b.mu.Lock()
	e, ok := b.entries[label]
	b.mu.Unlock()
	return ok && e.unread
}

func (b *basisRegistry) MarkRead(label string) {
	b.mu.Lock()
	if e, ok := b.entries[label]; ok && e.unread {
		e.unread = false
		b.entries[label] = e
	}
	b.mu.Unlock()
}

// Verify reports whether the basis names a capture this shell recorded of this
// very file (known) and whether that capture's content is still what the file
// holds (fresh).
func (b *basisRegistry) Verify(label, path, current string) (known, fresh bool) {
	if label == "" {
		return false, false
	}
	b.mu.Lock()
	e, ok := b.entries[label]
	b.mu.Unlock()
	if !ok || e.path != filepath.Clean(path) {
		return false, false
	}
	return true, e.hash == contentHash(current)
}

func (b *basisRegistry) Reset() {
	b.mu.Lock()
	b.entries = make(map[string]basisEntry)
	b.mu.Unlock()
}

func contentHash(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}
