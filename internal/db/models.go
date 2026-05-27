// Copyright 2026 The CALM Authors
// SPDX-License-Identifier: Apache-2.0

package db

import "time"

// DefaultClient is the DL01 bootstrap client name; sessions that omit `client`
// at creation attribute to it.
const DefaultClient = "default"

type ClientSummary struct {
	Name         string
	SessionCount int
	LastActivity *time.Time
}

type Session struct {
	ID           string
	Namespace    string
	Client       string
	CreatedAt    time.Time
	LastActivity time.Time
	// ExpiresAt is maintained by the sessions_set_expires_at trigger; never set from Go.
	ExpiresAt  time.Time
	TTLMinutes int
	Labels     map[string]string
}

type ManagedSession struct {
	ID           string
	Namespace    string
	Client       string
	CreatedAt    time.Time
	LastActivity time.Time
	ExpiresAt    time.Time
	TTLMinutes   int
	Labels       map[string]string
	EventCount   int
}

// SessionRef pairs (session_id, namespace) so the TTL scanner can call the
// namespace-scoped DeleteSession with the namespace it learned from the row.
type SessionRef struct {
	SessionID string
	Namespace string
}

type Chunk struct {
	Title       string
	Content     string
	ContentType string
}

type IndexInput struct {
	SessionID string
	Source    string
	Chunks    []Chunk
}

type SearchInput struct {
	SessionID string
	Queries   []string
	Source    string
	Limit     int
}

type SearchHit struct {
	Title      string
	Snippet    string
	Source     string
	MatchLayer string // "primary" | "trigram" | "fuzzy"
}

type SearchResult struct {
	Query string
	Hits  []SearchHit
}

type Event struct {
	ID        int64
	SessionID string
	Type      string
	Priority  int
	Data      []byte
	DataHash  string
	CreatedAt time.Time
}

type EventFilter struct {
	Types       []string
	MinPriority int
	Limit       int
}

type ListSessionsFilter struct {
	Namespace string
	Client    string
	Labels    map[string]string
}

type SourceSummary struct {
	Label     string
	Chunks    int
	IndexedAt time.Time
}

type CascadeCounts struct {
	Sources int
	Chunks  int
	Events  int
	Labels  int
}

type DeleteSessionResult struct {
	SessionID string
	Cascaded  CascadeCounts
}

type DeleteSessionsResult struct {
	DeletedSessions int
	Cascaded        CascadeCounts
}

type DeleteClientResult struct {
	Client          string
	DeletedSessions int
	Cascaded        CascadeCounts
}
