// Copyright 2026 The CALM Authors
// SPDX-License-Identifier: Apache-2.0

package session

import (
	"context"
	"time"

	"github.com/one-harsh/calm/internal/db"
)

// Service owns session-lifecycle orchestration: single-session ops
// (Create/Get/Touch/Delete), bulk admin ops (List/Count/DeleteAll), and the
// TTL-scanner entry point (ScanExpired). Handlers and the TTL scanner call
// Service, never *db.Store's repos directly — this is where cache
// invalidation, metrics, audit hooks, and cross-entity orchestration land.
type Service struct {
	store *db.Store
}

func New(store *db.Store) *Service {
	return &Service{store: store}
}

// Create is the DL01 auto-attribution boundary: client referenced by the
// session is registered (idempotently) if it doesn't exist, then the session
// is inserted — both inside a single transaction via Store.WithTx so partial
// failure rolls back cleanly (no orphan client row, no partial session state).
func (s *Service) Create(ctx context.Context, sess db.Session) error {
	if sess.ID == "" {
		return db.ErrSessionIDRequired
	}
	if sess.TTLMinutes <= 0 {
		return db.ErrInvalidTTL
	}
	if sess.Client == "" {
		sess.Client = db.DefaultClient
	}
	return s.store.WithTx(ctx, func(r db.Repos) error {
		if err := r.Clients.Register(ctx, sess.Namespace, sess.Client); err != nil {
			return err
		}
		return r.Sessions.Create(ctx, sess)
	})
}

func (s *Service) Get(ctx context.Context, namespace, id string) (db.Session, error) {
	return s.store.Sessions().Get(ctx, namespace, id)
}

func (s *Service) Touch(ctx context.Context, namespace, id string, lastActivity time.Time) error {
	return s.store.Sessions().Touch(ctx, namespace, id, lastActivity)
}

func (s *Service) Delete(ctx context.Context, namespace, id string) (db.DeleteSessionResult, error) {
	return s.store.Sessions().Delete(ctx, namespace, id)
}

func (s *Service) List(ctx context.Context, filter db.ListSessionsFilter) ([]db.ManagedSession, error) {
	return s.store.Sessions().List(ctx, filter)
}

func (s *Service) Count(ctx context.Context, filter db.ListSessionsFilter) (int, error) {
	return s.store.Sessions().Count(ctx, filter)
}

func (s *Service) DeleteAll(ctx context.Context, filter db.ListSessionsFilter) (db.DeleteSessionsResult, error) {
	return s.store.Sessions().DeleteAll(ctx, filter)
}

func (s *Service) ScanExpired(ctx context.Context, now time.Time) ([]db.SessionRef, error) {
	return s.store.Sessions().ScanExpired(ctx, now)
}
