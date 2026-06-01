// Copyright 2026 The CALM Authors
// SPDX-License-Identifier: Apache-2.0

package session

import (
	"context"
	"errors"
	"math/rand"
	"time"

	logging "github.com/one-harsh/context-logging"

	"github.com/one-harsh/calm/internal/db"
	"github.com/one-harsh/calm/internal/obs"
)

type expirySource interface {
	ScanExpired(ctx context.Context, now time.Time) ([]db.SessionRef, error)
	DeleteByID(ctx context.Context, namespace string, sessionID int64) (db.DeleteSessionResult, error)
}

var _ expirySource = (*Service)(nil)

type ScannerConfig struct {
	Interval time.Duration // base cadence; 0 disables the scanner entirely
	Jitter   time.Duration // ± window applied per tick; 0 means fixed cadence
}

type Scanner struct {
	sessions expirySource
	cfg      ScannerConfig
	logger   *logging.Logger
	rng      *rand.Rand
}

func NewScanner(sessions expirySource, cfg ScannerConfig, logger *logging.Logger) *Scanner {
	return &Scanner{
		sessions: sessions,
		cfg:      cfg,
		logger:   logger,
		rng:      rand.New(rand.NewSource(time.Now().UnixNano())), //nolint:gosec // cadence jitter, not crypto
	}
}

// Run blocks until ctx is cancelled; per-scan and per-ref errors log and continue.
func (s *Scanner) Run(ctx context.Context) error {
	if s.cfg.Interval <= 0 {
		s.logger.WithContext(ctx).Info("ttl scanner disabled (interval=0)")
		return nil
	}

	s.logger.WithContext(ctx).Info("ttl scanner started",
		logging.IntField("interval_ms", int(s.cfg.Interval/time.Millisecond)),
		logging.IntField("jitter_ms", int(s.cfg.Jitter/time.Millisecond)),
	)
	defer s.logger.WithContext(ctx).Info("ttl scanner stopped")

	for {
		delay := s.cfg.Interval
		if s.cfg.Jitter > 0 {
			delay += time.Duration(s.rng.Int63n(int64(2*s.cfg.Jitter+1))) - s.cfg.Jitter
		}
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(delay):
			s.scanOnce(ctx)
		}
	}
}

func (s *Scanner) scanOnce(ctx context.Context) {
	start := time.Now()
	refs, err := s.sessions.ScanExpired(ctx, start)
	if err != nil {
		s.logger.WithContext(ctx).Warn("ttl scan failed", logging.ErrorField(err))
		return
	}

	deleted := 0
	for _, ref := range refs {
		ctx := logging.Bind(ctx,
			obs.Namespace(ref.Namespace),
			obs.SessionID(ref.ID),
		)
		res, err := s.sessions.DeleteByID(ctx, ref.Namespace, ref.ID)
		if err != nil {
			if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
				s.logger.WithContext(ctx).Info("ttl scan canceled mid-delete")
				return
			}

			// Peer replica beat us to it — idempotent, no need to log.
			if errors.Is(err, db.ErrSessionNotFound) {
				continue
			}

			s.logger.WithContext(ctx).Warn("ttl delete failed",
				logging.ErrorField(err),
			)
			continue
		}
		s.logger.WithContext(ctx).Info("session closed",
			obs.CloseReasonTTLExpired,
			logging.IntField("session.delete.cascaded_events", res.Cascaded.Events),
			logging.IntField("session.delete.cascaded_sources", res.Cascaded.Sources),
			logging.IntField("session.delete.cascaded_chunks", res.Cascaded.Chunks),
			logging.IntField("session.delete.cascaded_labels", res.Cascaded.Labels),
		)
		deleted++
	}

	s.logger.WithContext(ctx).Debug("ttl scan complete",
		logging.IntField("sessions.scanned", len(refs)),
		logging.IntField("sessions.deleted", deleted),
		logging.IntField("ttl_scan.duration_ms", int(time.Since(start)/time.Millisecond)),
	)
}
