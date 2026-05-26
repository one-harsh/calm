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
// Service, never the db.SessionRepo directly — this is where cache
// invalidation, metrics, and audit hooks land as they come online.
type Service struct {
	repo db.SessionRepo
}

func New(repo db.SessionRepo) *Service {
	return &Service{repo: repo}
}

func (s *Service) Create(ctx context.Context, sess db.Session) error {
	return s.repo.Create(ctx, sess)
}

func (s *Service) Get(ctx context.Context, namespace, id string) (db.Session, error) {
	return s.repo.Get(ctx, namespace, id)
}

func (s *Service) Touch(ctx context.Context, namespace, id string, lastActivity time.Time) error {
	return s.repo.Touch(ctx, namespace, id, lastActivity)
}

func (s *Service) Delete(ctx context.Context, namespace, id string) (db.DeleteSessionResult, error) {
	return s.repo.Delete(ctx, namespace, id)
}

func (s *Service) List(ctx context.Context, filter db.ListSessionsFilter) ([]db.ManagedSession, error) {
	return s.repo.List(ctx, filter)
}

func (s *Service) Count(ctx context.Context, filter db.ListSessionsFilter) (int, error) {
	return s.repo.Count(ctx, filter)
}

func (s *Service) DeleteAll(ctx context.Context, filter db.ListSessionsFilter) (db.DeleteSessionsResult, error) {
	return s.repo.DeleteAll(ctx, filter)
}

func (s *Service) ScanExpired(ctx context.Context, now time.Time) ([]db.SessionRef, error) {
	return s.repo.ScanExpired(ctx, now)
}
