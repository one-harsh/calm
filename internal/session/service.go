// Copyright 2026 The CALM Authors
// SPDX-License-Identifier: Apache-2.0

package session

import (
	"context"
	"errors"
	"time"

	"github.com/one-harsh/calm/internal/db"
)

// Service is the only path handlers and the TTL scanner use to reach
// session state — cache invalidation, metrics, audit hooks, and cross-entity
// orchestration land here, never in *db.Store's repos directly.
type Service struct {
	store db.DAL
	cache cache
}

func New(store db.DAL, cacheSize int) *Service {
	return &Service{store: store, cache: newCache(cacheSize)}
}

// Create inserts the session row directly. The client must already be
// registered (via POST /v1/clients/{name} or via SeedDefaults for the
// default client); a missing client surfaces as ErrClientNotFound via the
// DAL's FK-violation translation. sess is normalized (default client) and
// enriched (CreatedAt from RETURNING) in place, so the handler can
// serialize the populated struct directly.
func (s *Service) Create(ctx context.Context, sess *db.Session) error {
	if sess.ID == "" {
		return db.ErrSessionIDRequired
	}
	if sess.TTLMinutes <= 0 {
		return db.ErrInvalidTTL
	}
	if sess.Client == "" {
		sess.Client = db.DefaultClient
	}
	if err := s.store.Sessions().Create(ctx, sess); err != nil {
		return err
	}

	// Just-created session is the next-request target; prime the cache.
	s.cache.Put(cacheKey{Namespace: sess.Namespace, SessionID: sess.ID}, SessionMetadata{
		Client:     sess.Client,
		TTLMinutes: sess.TTLMinutes,
		CreatedAt:  sess.CreatedAt,
	})
	return nil
}

func (s *Service) Lookup(ctx context.Context, namespace, id string) (SessionMetadata, error) {
	k := cacheKey{Namespace: namespace, SessionID: id}
	if md, ok := s.cache.Lookup(k); ok {
		return md, nil
	}
	sess, err := s.store.Sessions().Get(ctx, namespace, id)
	if err != nil {
		return SessionMetadata{}, err
	}
	md := SessionMetadata{
		Client:     sess.Client,
		TTLMinutes: sess.TTLMinutes,
		CreatedAt:  sess.CreatedAt,
	}
	s.cache.Put(k, md)
	return md, nil
}

func (s *Service) Get(ctx context.Context, namespace, id string) (db.Session, error) {
	return s.store.Sessions().Get(ctx, namespace, id)
}

func (s *Service) Touch(ctx context.Context, namespace, id string, lastActivity time.Time) error {
	err := s.store.Sessions().Touch(ctx, namespace, id, lastActivity)
	if errors.Is(err, db.ErrSessionNotFound) {
		s.cache.Invalidate(cacheKey{Namespace: namespace, SessionID: id})
	}
	return err
}

func (s *Service) Delete(ctx context.Context, namespace, id string) (db.DeleteSessionResult, error) {
	result, err := s.store.Sessions().Delete(ctx, namespace, id)
	if err == nil || errors.Is(err, db.ErrSessionNotFound) {
		s.cache.Invalidate(cacheKey{Namespace: namespace, SessionID: id})
	}
	return result, err
}

func (s *Service) List(ctx context.Context, filter db.ListSessionsFilter) ([]db.ManagedSession, error) {
	return s.store.Sessions().List(ctx, filter)
}

func (s *Service) Count(ctx context.Context, filter db.ListSessionsFilter) (int, error) {
	return s.store.Sessions().Count(ctx, filter)
}

func (s *Service) DeleteAll(ctx context.Context, filter db.ListSessionsFilter) (db.DeleteSessionsResult, error) {
	result, err := s.store.Sessions().DeleteAll(ctx, filter)
	if err != nil {
		return result, err
	}

	// DAL.DeleteAll doesn't return per-row IDs, so over-evict by namespace
	// and let siblings refill on demand. Purge is defense-in-depth — empty
	// namespace is rejected upstream.
	if filter.Namespace != "" {
		s.cache.InvalidateNamespace(filter.Namespace)
	} else {
		s.cache.Purge()
	}
	return result, nil
}

func (s *Service) ScanExpired(ctx context.Context, now time.Time) ([]db.SessionRef, error) {
	return s.store.Sessions().ScanExpired(ctx, now)
}
