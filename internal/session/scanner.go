// Copyright 2026 The CALM Authors
// SPDX-License-Identifier: Apache-2.0

// Scanner is CALM's TTL-expiry reaper: a per-replica goroutine that
// periodically calls Service.ScanExpired and then Service.Delete per
// returned ref. One delete path; the scanner is just another caller of it,
// so cache invalidation, cascade counts, and the structured INFO log fall
// out automatically.
//
// Why polling, not Postgres-side cron. TTL expiry is time-based, not
// event-based — nothing happens when a session crosses its TTL except
// wall-clock advancing past a threshold. That rules out plain LISTEN/NOTIFY
// and AFTER-UPDATE triggers as primary expiry signals (both fire on row
// writes). The real alternative is pg_cron + AFTER-DELETE trigger +
// LISTEN/NOTIFY, where Go listeners receive deletion events and run the
// same cache-invalidate + structured-log code path. That approach is
// technically equivalent on the invariant front: cache, log, and cascade
// counts can all be reconstructed Go-side from the NOTIFY payload.
//
// The honest comparison comes down to one architectural asymmetry that
// matters and a bundle of operational costs that don't, at v1 scale:
//
//   - **N-pod read cost (the asymmetry).** With this polling design every
//     replica does the scan every interval — N indexed queries per minute
//     against `sessions`, which is also the hot table behind every cache-
//     miss Lookup, every Touch (with its expires_at trigger + index update),
//     and every Create. pg_cron with a single cluster-wide scheduler does
//     one read per interval, period. At 1–3 replicas (typical sidecar
//     deploy) the polling fan-out is below noise; at 10+ replicas it
//     becomes measurable buffer-pool / autovacuum pressure on a hot table.
//   - **Operational costs of pg_cron.** Fourth required extension (alongside
//     pg_search/pg_textsearch and pg_trgm); split control plane (cron.schedule
//     SQL state vs CALM YAML for everything else); distributed-listener
//     fragility (per-replica long-lived LISTEN connections silently stop
//     delivering on connection drops); NOTIFY payload size ceiling (8 KB);
//     failure-mode split (the reaper runs even when CALM is fully down —
//     pro during long outages, con during debug sessions where you'd like
//     to pause cleanup by stopping CALM).
//
// We pick polling because the upgrade path inside this design is short and
// well-understood: when the N-pod read cost becomes load-visible, wrap each
// tick in `pg_try_advisory_lock(scannerKey)` so one of the N replicas acts
// as the de-facto leader per scan and others see the lock held and skip.
// ~30 LOC, zero new dependencies, layers onto this file without a rewrite.
// The end state is the same cluster-wide read cost as pg_cron, paid only
// when scale demands it. The HLD explicitly accepts the redundant-scan
// regime in the meantime ("redundant queries, not conflicts" — DELETE is
// idempotent).
//
// So: pg_cron solves multi-pod by architecture; polling solves it later via
// advisory lock if needed. We defer the complexity until justified by load
// rather than paying it upfront for a deploy shape (1–3 replicas) where it
// returns nothing.

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

// expirySource is the narrow port the scanner consumes. *Service satisfies
// it; mockery generates MockExpirySource for unit tests. Keeping the
// interface scanner-local matches the existing DAL-port pattern: ports
// live where they're consumed.
type expirySource interface {
	ScanExpired(ctx context.Context, now time.Time) ([]db.SessionRef, error)
	Delete(ctx context.Context, namespace, id string) (db.DeleteSessionResult, error)
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

// Run blocks until ctx is cancelled. Errors during a scan (ScanExpired
// failure or per-ref Delete failure) are logged and the loop continues —
// one bad row must not abort cleanup of the rest.
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

// scanOnce runs a single scan + delete pass.
// Drains all returned refs.
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
			obs.SessionID(ref.SessionID),
		)
		res, err := s.sessions.Delete(ctx, ref.Namespace, ref.SessionID)
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
