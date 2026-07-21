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
	ID               int64
	SessionToken     string
	SessionTokenHash []byte
	Namespace        string
	Client           string
	CreatedAt        time.Time
	LastActivity     time.Time
	// ExpiresAt is maintained by the sessions_set_expires_at trigger; never set from Go.
	ExpiresAt  time.Time
	TTLMinutes int
	Labels     map[string]string
}

func (s Session) SessionID() int64 { return s.ID }

type ManagedSession struct {
	ID           int64
	Namespace    string
	Client       string
	CreatedAt    time.Time
	LastActivity time.Time
	ExpiresAt    time.Time
	TTLMinutes   int
	Labels       map[string]string
	EventCount   int
}

type SessionRef struct {
	ID        int64
	Namespace string
}

type Chunk struct {
	Title       string
	Content     string
	ContentType string
}

type SearchInput struct {
	SessionID int64
	Queries   []string
	Source    string
	Limit     int
}

type SearchHit struct {
	Title      string
	Snippet    string
	Source     string
	MatchLayer string // "primary" | "trigram"
	// SnippetFallback marks a hit whose snippet is a leading-window fallback:
	// no query term was literally located in the (over-budget) chunk content.
	// The search service aggregates this into the snippet_fallbacks correlation
	// dimension. db.SearchHit is never marshaled — the wire type genapi.SearchHit
	// carries no such field, so this stays an internal signal.
	SnippetFallback bool
}

type SearchResult struct {
	Query string
	Hits  []SearchHit
}

type Event struct {
	ID        int64
	SessionID int64
	Type      string
	Priority  int
	Data      []byte
	DataHash  []byte
	CreatedAt time.Time
}

type EventFilter struct {
	Types       []string
	MinPriority int
	Limit       int
}

type EventInput struct {
	Type     string
	Priority int
	Data     []byte
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
	ID       int64
	Cascaded CascadeCounts
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

type CorrelationRecord struct {
	CorrelationID      []byte
	Namespace          string
	SessionID          int64
	RequestType        string
	RequestMeta        []byte
	Outcome            string
	FeedbackReceivedAt time.Time
	CreatedAt          time.Time
}
