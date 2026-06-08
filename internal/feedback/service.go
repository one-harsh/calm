// Copyright 2026 The CALM Authors
// SPDX-License-Identifier: Apache-2.0

package feedback

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	logging "github.com/one-harsh/context-logging"

	"github.com/one-harsh/calm/internal/db"
)

var (
	ErrFeedbackWindowExpired = errors.New("feedback: window expired")
	ErrInvalidCorrelationID  = errors.New("feedback: invalid correlation id")
)

type Service struct {
	store  db.DAL
	logger *logging.Logger
	now    func() time.Time
}

func New(store db.DAL, logger *logging.Logger) *Service {
	return &Service{
		store:  store,
		logger: logger,
		now:    time.Now,
	}
}

func (s *Service) Receive(ctx context.Context, namespace string, sessionID int64, correlationID uuid.UUID, outcome string, ttlMinutes int) (db.CorrelationRecord, error) {
	if correlationID.Version() != 7 {
		return db.CorrelationRecord{}, fmt.Errorf("%w: version %d", ErrInvalidCorrelationID, correlationID.Version())
	}

	sec, nsec := correlationID.Time().UnixTime()
	embedded := time.Unix(sec, nsec)
	if s.now().Sub(embedded) > time.Duration(ttlMinutes)*time.Minute {
		return db.CorrelationRecord{}, ErrFeedbackWindowExpired
	}

	var rec db.CorrelationRecord
	err := s.store.WithTx(ctx, func(r db.Repos) error {
		existing, err := r.Correlations.GetWithLockedRow(ctx, namespace, sessionID, correlationID[:])
		if err != nil {
			return err
		}
		if !existing.FeedbackReceivedAt.IsZero() {
			return db.ErrFeedbackAlreadySubmitted
		}
		updated, err := r.Correlations.UpdateOutcome(ctx, namespace, sessionID, correlationID[:], outcome)
		if err != nil {
			return err
		}
		rec = updated
		return nil
	})
	return rec, err
}
