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
	Delete(ctx context.Context, namespace, name string) (DeleteClientResult, error)
}

type SessionRepo interface {
	Create(ctx context.Context, sess *Session) error
	Get(ctx context.Context, namespace string, sessionTokenHash []byte) (Session, error)
	Touch(ctx context.Context, namespace string, sessionTokenHash []byte, lastActivity time.Time) error
	Delete(ctx context.Context, namespace string, sessionTokenHash []byte) (DeleteSessionResult, error)
	DeleteByID(ctx context.Context, namespace string, sessionID int64) (DeleteSessionResult, error)
	List(ctx context.Context, filter ListSessionsFilter) ([]ManagedSession, error)
	Count(ctx context.Context, filter ListSessionsFilter) (int, error)
	DeleteAll(ctx context.Context, filter ListSessionsFilter) (DeleteSessionsResult, error)
	ScanExpired(ctx context.Context, now time.Time) ([]SessionRef, error)
}

type EventsRepo interface {
	Write(ctx context.Context, namespace string, sessionID int64, events []EventInput) (accepted int, err error)
	Read(ctx context.Context, namespace string, sessionID int64, filter EventFilter) ([]Event, error)
	Snapshot(ctx context.Context, namespace string, sessionID int64) ([]Event, error)
}

type DAL interface {
	Clients() ClientRepo
	Sessions() SessionRepo
	Events() EventsRepo
	WithTx(ctx context.Context, fn func(Repos) error) error
}

var (
	_ ClientRepo  = (*clientRepo)(nil)
	_ SessionRepo = (*sessionRepo)(nil)
	_ EventsRepo  = (*eventsRepo)(nil)
	_ DAL         = (*Store)(nil)
)
