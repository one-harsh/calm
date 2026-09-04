// Copyright 2026 The CALM Authors
// SPDX-License-Identifier: Apache-2.0

package session

import (
	"context"
	"errors"
	"fmt"
	"time"

	logging "github.com/one-harsh/context-logging"

	"github.com/one-harsh/calm/internal/auth"
	"github.com/one-harsh/calm/internal/db"
	"github.com/one-harsh/calm/internal/obs"
)

type Service struct {
	store       db.DAL
	cache       cache
	idempotency *idempotencyStore
	logger      *logging.Logger
}

type Config struct {
	CacheSize          int
	IdempotencyKeyTTL  time.Duration
	IdempotencyKeySize int
}

func New(store db.DAL, cfg Config, logger *logging.Logger) *Service {
	return &Service{
		store:       store,
		cache:       newCache(cfg.CacheSize),
		idempotency: newIdempotencyStore(cfg.IdempotencyKeySize, cfg.IdempotencyKeyTTL, time.Now),
		logger:      logger,
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
			if s.logger.Enabled(logging.DebugLevel) {
				s.logger.WithContext(ctx).Debug(
					"session create deduped to existing",
					obs.Client(sess.Client),
					obs.SessionID(cached.SessionID),
				)
			}
			return cached, nil
		}
		raw, err := auth.NewRandomToken()
		if err != nil {
			return dedupEntry{}, fmt.Errorf("generate session token: %w", err)
		}
		sess.SessionToken = raw
		sess.SessionTokenHash = auth.HashToken(sess.Namespace, raw)

		if err := s.store.WithTx(ctx, func(r db.Repos) error {
			if err := r.Sessions.Insert(ctx, sess); err != nil {
				return err
			}
			return r.Sessions.InsertLabels(ctx, sess.ID, sess.Labels)
		}); err != nil {
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
	var result db.DeleteSessionResult
	err := s.store.WithTx(ctx, func(r db.Repos) error {
		id, client, err := r.Sessions.LockByTokenHash(ctx, namespace, auth.HashToken(namespace, sessionToken))
		if err != nil {
			return err
		}
		result.ID = id
		return s.deleteLockedSession(ctx, r, namespace, client, id, &result.Cascaded, time.Now().UTC())
	})
	if err == nil || errors.Is(err, db.ErrSessionNotFound) {
		s.cache.Invalidate(cacheKey{Namespace: namespace, SessionToken: sessionToken})
	}
	if err != nil {
		return db.DeleteSessionResult{}, err
	}
	return result, nil
}

// Scanner has no raw token, so this evicts by namespace.
func (s *Service) DeleteByID(ctx context.Context, namespace string, sessionID int64) (db.DeleteSessionResult, error) {
	result := db.DeleteSessionResult{ID: sessionID}
	err := s.store.WithTx(ctx, func(r db.Repos) error {
		client, lastActivity, err := r.Sessions.LockByID(ctx, namespace, sessionID)
		if err != nil {
			return err
		}
		// Scanner is not client activity — bump to session.last_activity
		// (the real work) rather than now (the scan moment).
		return s.deleteLockedSession(ctx, r, namespace, client, sessionID, &result.Cascaded, lastActivity)
	})
	if namespace != "" {
		s.cache.InvalidateNamespace(namespace)
	}
	if err != nil {
		return db.DeleteSessionResult{}, err
	}
	return result, nil
}

// deleteLockedSession runs the cascade choreography for an already-locked session:
// count children, bump the owning client's activity, delete the row (children go
// via ON DELETE CASCADE). Must run inside a WithTx that holds the lock.
func (s *Service) deleteLockedSession(ctx context.Context, r db.Repos, namespace, client string, sessionID int64, cascaded *db.CascadeCounts, activityAt time.Time) error {
	s.logger.WithContext(ctx).Debug("delete session: lock acquired", obs.SessionID(sessionID))
	cascade, err := r.Sessions.CascadeCounts(ctx, sessionID)
	if err != nil {
		return err
	}
	*cascaded = cascade
	s.logger.WithContext(ctx).Debug(
		"delete session: cascade computed",
		logging.IntField("sources", cascade.Sources),
		logging.IntField("chunks", cascade.Chunks),
		logging.IntField("events", cascade.Events),
		logging.IntField("labels", cascade.Labels),
	)
	if err := r.Clients.BumpActivity(ctx, namespace, client, activityAt); err != nil {
		return err
	}
	if err := r.Sessions.DeleteByIDRow(ctx, sessionID); err != nil {
		return err
	}
	s.logger.WithContext(ctx).Debug("delete session: committed", obs.SessionID(sessionID))
	return nil
}

func (s *Service) List(ctx context.Context, filter db.ListSessionsFilter) ([]db.ManagedSession, error) {
	return s.store.Sessions().List(ctx, filter)
}

func (s *Service) Count(ctx context.Context, filter db.ListSessionsFilter) (int, error) {
	return s.store.Sessions().Count(ctx, filter)
}

func (s *Service) DeleteAll(ctx context.Context, filter db.ListSessionsFilter) (db.DeleteSessionsResult, error) {
	var result db.DeleteSessionsResult
	err := s.store.WithTx(ctx, func(r db.Repos) error {
		ids, err := r.Sessions.LockAllByFilter(ctx, filter)
		if err != nil {
			return err
		}
		s.logger.WithContext(ctx).Debug("delete sessions: locked rows", logging.IntField("count", len(ids)))
		if len(ids) == 0 {
			return nil
		}
		result.DeletedSessions = len(ids)
		cascade, err := r.Sessions.CascadeCountsForIDs(ctx, ids)
		if err != nil {
			return err
		}
		result.Cascaded = cascade
		s.logger.WithContext(ctx).Debug(
			"delete sessions: cascade computed",
			logging.IntField("sources", cascade.Sources),
			logging.IntField("chunks", cascade.Chunks),
			logging.IntField("events", cascade.Events),
			logging.IntField("labels", cascade.Labels),
		)
		if err := r.Sessions.DeleteRows(ctx, ids); err != nil {
			return err
		}
		s.logger.WithContext(ctx).Debug("delete sessions: committed")
		return nil
	})
	if err != nil {
		return db.DeleteSessionsResult{}, err
	}

	// DeleteAll doesn't track per-row tokens, so over-evict by namespace.
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

func (s *Service) InvalidateNamespaceCache(namespace string) {
	s.cache.InvalidateNamespace(namespace)
}
