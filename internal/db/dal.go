// Copyright 2026 The CALM Authors
// SPDX-License-Identifier: Apache-2.0

package db

import (
	"context"
	"errors"
	"time"
)

// Every error returned by the DAL must wrap one of these sentinels (directly
// for validation/domain errors, via multi-%w with ErrStorageBackend for
// driver/tx failures) so callers can classify via errors.Is without string
// matching.
//
// Sentinel-first wrap form: the sentinel %w goes at the front of fmt.Errorf
// so the final error string starts with "db: ...". 1-glance log/grep scans
// can identify a DAL error from the prefix without parsing.
//
//	fmt.Errorf("%w: register client %q/%q: %w", ErrStorageBackend, ns, name, err)
//	// → "db: storage backend failure: register client \"ns-a\"/\"alice\": <underlying>"
//
// Every sentinel below starts with "db: " for the same reason.
var (
	ErrNotImplemented = errors.New("db: not implemented")

	// Validation — required input was missing or invalid.

	ErrNamespaceRequired  = errors.New("db: namespace is required")
	ErrClientNameRequired = errors.New("db: client name is required")
	ErrSessionIDRequired  = errors.New("db: session_id is required")
	ErrSourceRequired     = errors.New("db: source label is required")
	ErrChunksRequired     = errors.New("db: index input has no chunks")
	ErrQueryRequired      = errors.New("db: search query is empty")
	ErrInvalidLimit       = errors.New("db: search limit must be positive")
	ErrInvalidPriority    = errors.New("db: event priority must be 1..4")
	ErrInvalidTTL         = errors.New("db: ttl_minutes must be positive")

	// Domain — business-meaningful states.

	ErrSessionNotFound = errors.New("db: session not found")
	ErrSessionExists   = errors.New("db: session already exists")
	ErrClientNotFound  = errors.New("db: client not found")
	ErrClientProtected = errors.New("db: cannot delete the default client")

	// Storage — umbrella for driver/tx failures. Always wrapped via
	// fmt.Errorf("%w: ...: %w", ErrStorageBackend, ..., underlying) so callers
	// can errors.Is(err, ErrStorageBackend) and errors.As(err, &pgErr).
	ErrStorageBackend = errors.New("db: storage backend failure")
)

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
	TTLMinutes   int
	Labels       map[string]string
}

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

// DAL is the storage port. Mockery generates an in-package mock against this
// interface (mock_dal.go, build-tag "mocks"). Single backend (Postgres); the
// abstraction exists for testability, not portability.
type DAL interface {
	RegisterClient(ctx context.Context, namespace, name string) error
	ListClients(ctx context.Context, namespace string) ([]ClientSummary, error)
	CountClientSessions(ctx context.Context, namespace, name string) (int, error)
	DeleteClient(ctx context.Context, namespace, name string) (DeleteClientResult, error)

	CreateSession(ctx context.Context, s Session) error
	GetSession(ctx context.Context, namespace, id string) (Session, error)
	TouchSession(ctx context.Context, namespace, id string, lastActivity time.Time) error
	ListManagedSessions(ctx context.Context, filter ListSessionsFilter) ([]ManagedSession, error)
	CountSessions(ctx context.Context, filter ListSessionsFilter) (int, error)
	DeleteSession(ctx context.Context, namespace, id string) (DeleteSessionResult, error)
	DeleteSessions(ctx context.Context, filter ListSessionsFilter) (DeleteSessionsResult, error)
	ScanExpiredSessions(ctx context.Context, now time.Time) ([]SessionRef, error)

	Index(ctx context.Context, in IndexInput) error
	Search(ctx context.Context, in SearchInput) ([]SearchResult, error)
	ListSources(ctx context.Context, sessionID string) ([]SourceSummary, error)

	WriteEvents(ctx context.Context, sessionID string, events []Event) (int, error)
	ReadEvents(ctx context.Context, sessionID string, filter EventFilter) ([]Event, error)

	Close() error
}
