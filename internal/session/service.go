// Copyright 2026 The CALM Authors
// SPDX-License-Identifier: Apache-2.0

package session

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/one-harsh/calm/internal/auth"
	"github.com/one-harsh/calm/internal/db"
)

type Service struct {
	store       db.DAL
	cache       cache
	idempotency *idempotencyStore
}

type Config struct {
	CacheSize          int
	IdempotencyKeyTTL  time.Duration
	IdempotencyKeySize int
}

func New(store db.DAL, cfg Config) *Service {
	return &Service{
		store:       store,
		cache:       newCache(cfg.CacheSize),
		idempotency: newIdempotencyStore(cfg.IdempotencyKeySize, cfg.IdempotencyKeyTTL, time.Now),
	}
}

func (s *Service) Create(ctx context.Context, sess *db.Session, idempotencyKey string) error {
	if sess.Namespace == "" {
		return db.ErrNamespaceRequired
	}
	if sess.TTLMinutes <= 0 {
		return db.ErrInvalidTTL
	}
	if sess.Client == "" {
		sess.Client = db.DefaultClient
	}

	entry, err := s.idempotency.Do(sess.Namespace, sess.Client, idempotencyKey, func() (dedupEntry, error) {
		if cached, ok := s.idempotency.Resolve(sess.Namespace, sess.Client, idempotencyKey); ok {
			return cached, nil
		}
		raw, err := auth.NewRandomToken()
		if err != nil {
			return dedupEntry{}, fmt.Errorf("generate session token: %w", err)
		}
		sess.SessionToken = raw
		sess.SessionTokenHash = auth.HashToken(sess.Namespace, raw)

		if err := s.store.Sessions().Create(ctx, sess); err != nil {
			return dedupEntry{}, err
		}

		s.cache.Put(cacheKey{Namespace: sess.Namespace, SessionToken: raw}, SessionMetadata{
			ID:         sess.ID,
			Client:     sess.Client,
			TTLMinutes: sess.TTLMinutes,
			CreatedAt:  sess.CreatedAt,
		})
		fresh := dedupEntry{
			SessionToken: sess.SessionToken,
			SessionID:    sess.ID,
			Client:       sess.Client,
			TTLMinutes:   sess.TTLMinutes,
			CreatedAt:    sess.CreatedAt,
		}
		s.idempotency.Store(sess.Namespace, idempotencyKey, fresh)
		return fresh, nil
	})
	if err != nil {
		return err
	}
	// Concurrent retries received the originator's entry from singleflight
	// and still hold their own request fields in sess; overwrite from entry.
	sess.SessionToken = entry.SessionToken
	sess.ID = entry.SessionID
	sess.Client = entry.Client
	sess.TTLMinutes = entry.TTLMinutes
	sess.CreatedAt = entry.CreatedAt
	return nil
}

func (s *Service) Lookup(ctx context.Context, namespace, sessionToken string) (SessionMetadata, error) {
	k := cacheKey{Namespace: namespace, SessionToken: sessionToken}
	if md, ok := s.cache.Lookup(k); ok {
		return md, nil
	}
	sess, err := s.store.Sessions().Get(ctx, namespace, auth.HashToken(namespace, sessionToken))
	if err != nil {
		return SessionMetadata{}, err
	}
	md := SessionMetadata{
		ID:         sess.ID,
		Client:     sess.Client,
		TTLMinutes: sess.TTLMinutes,
		CreatedAt:  sess.CreatedAt,
	}
	s.cache.Put(k, md)
	return md, nil
}

func (s *Service) Get(ctx context.Context, namespace, sessionToken string) (db.Session, error) {
	return s.store.Sessions().Get(ctx, namespace, auth.HashToken(namespace, sessionToken))
}

func (s *Service) Touch(ctx context.Context, namespace, sessionToken string, lastActivity time.Time) error {
	err := s.store.Sessions().Touch(ctx, namespace, auth.HashToken(namespace, sessionToken), lastActivity)
	if errors.Is(err, db.ErrSessionNotFound) {
		s.cache.Invalidate(cacheKey{Namespace: namespace, SessionToken: sessionToken})
	}
	return err
}

func (s *Service) Delete(ctx context.Context, namespace, sessionToken string) (db.DeleteSessionResult, error) {
	result, err := s.store.Sessions().Delete(ctx, namespace, auth.HashToken(namespace, sessionToken))
	if err == nil || errors.Is(err, db.ErrSessionNotFound) {
		s.cache.Invalidate(cacheKey{Namespace: namespace, SessionToken: sessionToken})
	}
	return result, err
}

// Scanner has no raw token, so this evicts by namespace.
func (s *Service) DeleteByID(ctx context.Context, namespace string, sessionID int64) (db.DeleteSessionResult, error) {
	result, err := s.store.Sessions().DeleteByID(ctx, namespace, sessionID)
	if namespace != "" {
		s.cache.InvalidateNamespace(namespace)
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

	// DAL.DeleteAll doesn't return per-row tokens, so over-evict by namespace.
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
