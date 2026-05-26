// Copyright 2026 The CALM Authors
// SPDX-License-Identifier: Apache-2.0

package db

import (
	"context"
	"time"
)

// ClientRepo is the per-entity port for client-table operations. Backed by
// *Store via Store.Clients(). Mockery generates MockClientRepo
// (mock_client_repo.go, build-tag "mocks") for unit tests that need a fake.
type ClientRepo interface {
	Register(ctx context.Context, namespace, name string) error
	List(ctx context.Context, namespace string) ([]ClientSummary, error)
	CountSessions(ctx context.Context, namespace, name string) (int, error)
	Delete(ctx context.Context, namespace, name string) (DeleteClientResult, error)
}

// SessionRepo is the per-entity port for session-table operations. Backed by
// *Store via Store.Sessions(). Mockery generates MockSessionRepo
// (mock_session_repo.go, build-tag "mocks") for unit tests that need a fake.
type SessionRepo interface {
	Create(ctx context.Context, sess Session) error
	Get(ctx context.Context, namespace, id string) (Session, error)
	Touch(ctx context.Context, namespace, id string, lastActivity time.Time) error
	Delete(ctx context.Context, namespace, id string) (DeleteSessionResult, error)
	List(ctx context.Context, filter ListSessionsFilter) ([]ManagedSession, error)
	Count(ctx context.Context, filter ListSessionsFilter) (int, error)
	DeleteAll(ctx context.Context, filter ListSessionsFilter) (DeleteSessionsResult, error)
	ScanExpired(ctx context.Context, now time.Time) ([]SessionRef, error)
}

var (
	_ ClientRepo  = (*clientRepo)(nil)
	_ SessionRepo = (*sessionRepo)(nil)
)
