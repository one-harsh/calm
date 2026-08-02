// Copyright 2026 The CALM Authors
// SPDX-License-Identifier: Apache-2.0

package db

import (
	"context"
	"time"
)

type ClientRepo interface {
	// Register returns created=true on the first call; created=false on duplicates (no error).
	Register(ctx context.Context, namespace, name string) (created bool, err error)

	// RegisterWithCredential errors with ErrClientExists on duplicate (no re-issue; token is unrecoverable post-hash).
	RegisterWithCredential(ctx context.Context, namespace, name string, tokenHash []byte) error

	RotateCredential(ctx context.Context, namespace, name string, newHash []byte) error
	LookupByToken(ctx context.Context, namespace string, tokenHash []byte) (name string, err error)
	List(ctx context.Context, namespace string) ([]ClientSummary, error)
	CountSessions(ctx context.Context, namespace, name string) (int, error)

	// Client-delete cascade primitives — composed in clientreg's WithTx.
	LockByName(ctx context.Context, namespace, name string) error
	CascadeCountsForClient(ctx context.Context, namespace, name string) (deletedSessions int, c CascadeCounts, err error)
	DeleteRow(ctx context.Context, namespace, name string) error
	BumpActivity(ctx context.Context, namespace, name string, ts time.Time) error
}

type SessionRepo interface {
	Insert(ctx context.Context, sess *Session) error
	InsertLabels(ctx context.Context, sessionID int64, labels map[string]string) error
	Get(ctx context.Context, namespace string, sessionTokenHash []byte) (Session, error)
	Touch(ctx context.Context, namespace string, sessionTokenHash []byte, lastActivity time.Time) error
	List(ctx context.Context, filter ListSessionsFilter) ([]ManagedSession, error)
	Count(ctx context.Context, filter ListSessionsFilter) (int, error)
	ScanExpired(ctx context.Context, now time.Time) ([]SessionRef, error)

	// Session-delete cascade primitives — composed in the session service's WithTx.
	LockByTokenHash(ctx context.Context, namespace string, sessionTokenHash []byte) (id int64, client string, err error)
	LockByID(ctx context.Context, namespace string, sessionID int64) (client string, lastActivity time.Time, err error)
	LockAllByFilter(ctx context.Context, filter ListSessionsFilter) ([]int64, error)
	CascadeCounts(ctx context.Context, sessionID int64) (CascadeCounts, error)
	CascadeCountsForIDs(ctx context.Context, ids []int64) (CascadeCounts, error)
	DeleteByIDRow(ctx context.Context, sessionID int64) error
	DeleteRows(ctx context.Context, ids []int64) error
}

type EventsRepo interface {
	Write(ctx context.Context, namespace string, sessionID int64, events []EventInput) (accepted int, err error)
	Read(ctx context.Context, namespace string, sessionID int64, filter EventFilter) ([]Event, error)
	Snapshot(ctx context.Context, namespace string, sessionID int64) ([]Event, error)
}

type SourcesRepo interface {
	Upsert(ctx context.Context, namespace string, sessionID int64, source string) (sourceID int64, created bool, err error)
	List(ctx context.Context, namespace string, sessionID int64) ([]SourceSummary, error)
	Search(ctx context.Context, namespace string, in SearchInput) ([]SearchHit, error)
	ChunksInOrder(ctx context.Context, namespace string, in DocOrderInput) (chunks []DocChunk, hasMore bool, err error)
}

type ChunksRepo interface {
	DeleteForSource(ctx context.Context, sourceID int64) error
	Insert(ctx context.Context, sourceID int64, chunks []Chunk) error
}

type VocabularyRepo interface {
	DecrementForSource(ctx context.Context, sourceID int64) error
	IncrementForSource(ctx context.Context, sourceID int64) error
	PruneZeros(ctx context.Context, sessionID int64) error
	TopByIDF(ctx context.Context, sessionID int64, limit int) ([]string, error)
}

type CorrelationsRepo interface {
	Insert(ctx context.Context, namespace string, sessionID int64, correlationID []byte, requestType string, requestMeta []byte) error
	GetWithLockedRow(ctx context.Context, namespace string, sessionID int64, correlationID []byte) (CorrelationRecord, error)
	UpdateOutcome(ctx context.Context, namespace string, sessionID int64, correlationID []byte, outcome string) (CorrelationRecord, error)
}

type DAL interface {
	Clients() ClientRepo
	Sessions() SessionRepo
	Events() EventsRepo
	Sources() SourcesRepo
	Chunks() ChunksRepo
	Vocabulary() VocabularyRepo
	Correlations() CorrelationsRepo
	WithTx(ctx context.Context, fn func(Repos) error) error
}

var (
	_ ClientRepo       = (*clientRepo)(nil)
	_ SessionRepo      = (*sessionRepo)(nil)
	_ EventsRepo       = (*eventsRepo)(nil)
	_ SourcesRepo      = (*sourcesRepo)(nil)
	_ ChunksRepo       = (*chunksRepo)(nil)
	_ VocabularyRepo   = (*vocabularyRepo)(nil)
	_ CorrelationsRepo = (*correlationsRepo)(nil)
	_ DAL              = (*Store)(nil)
)
