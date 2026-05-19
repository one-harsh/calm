// Copyright 2026 The CALM Authors
// SPDX-License-Identifier: Apache-2.0

package db

import (
	"context"
	"errors"
	"time"
)

var (
	ErrNotImplemented    = errors.New("db: not implemented")
	ErrSessionNotFound   = errors.New("db: session not found")
	ErrSessionExists     = errors.New("db: session already exists")
	ErrNamespaceRequired = errors.New("db: namespace is required")
	ErrEmptyChunks       = errors.New("db: index input has no chunks")
	ErrEmptyQuery        = errors.New("db: search query is empty")
	ErrInvalidLimit      = errors.New("db: search limit must be positive")
	ErrInvalidPriority   = errors.New("db: event priority must be 1..4")
	ErrClientNotFound    = errors.New("db: client not found")
	ErrClientProtected   = errors.New("db: cannot delete the default client")
)

// DefaultClient is the bootstrap client name assigned to sessions that omit
// the `client` field at creation (HLD DL01).
const DefaultClient = "default"

// ClientSummary is the per-client aggregate returned by ListClients
// (powers GET /v1/manage/clients).
type ClientSummary struct {
	Name         string
	SessionCount int
	LastActivity *time.Time
}

// Session is the row shape used by CreateSession / GetSession / TouchSession.
// Labels are populated for GetSession; CreateSession reads them from the input.
type Session struct {
	ID           string
	Namespace    string
	Client       string
	CreatedAt    time.Time
	LastActivity time.Time
	TTLMinutes   int
	Labels       map[string]string
}

// ManagedSession is the richer shape returned by the management API list
// endpoint — includes labels and aggregated event count.
type ManagedSession struct {
	ID           string
	Namespace    string
	Client       string
	CreatedAt    time.Time
	LastActivity time.Time
	TTLMinutes   int
	Labels       map[string]string
	EventCount   int
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

// CascadeCounts mirrors the cascade body shape in delete responses.
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

// DAL is the storage port. Mockery generates an in-package mock against this
// interface (mock_dal.go, build-tag "mocks"). Single backend (Postgres);
// the abstraction exists for testability, not portability.
type DAL interface {
	RegisterClient(ctx context.Context, namespace, name string) error
	ListClients(ctx context.Context, namespace string) ([]ClientSummary, error)
	CountClientSessions(ctx context.Context, namespace, name string) (int, error)
	DeleteClient(ctx context.Context, namespace, name string) (DeleteClientResult, error)

	CreateSession(ctx context.Context, s Session) error
	GetSession(ctx context.Context, id string) (Session, error)
	TouchSession(ctx context.Context, id string, lastActivity time.Time) error
	ListManagedSessions(ctx context.Context, filter ListSessionsFilter) ([]ManagedSession, error)
	CountSessions(ctx context.Context, filter ListSessionsFilter) (int, error)
	DeleteSession(ctx context.Context, id string) (DeleteSessionResult, error)
	DeleteSessions(ctx context.Context, filter ListSessionsFilter) (DeleteSessionsResult, error)
	ScanExpiredSessions(ctx context.Context, now time.Time) ([]string, error)

	Index(ctx context.Context, in IndexInput) error
	Search(ctx context.Context, in SearchInput) ([]SearchResult, error)
	ListSources(ctx context.Context, sessionID string) ([]SourceSummary, error)

	WriteEvents(ctx context.Context, sessionID string, events []Event) (int, error)
	ReadEvents(ctx context.Context, sessionID string, filter EventFilter) ([]Event, error)

	Close() error
}
