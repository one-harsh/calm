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
	correlations db.CorrelationsRepo
	logger       *logging.Logger
	now          func() time.Time
}

func New(correlations db.CorrelationsRepo, logger *logging.Logger) *Service {
	return &Service{
		correlations: correlations,
		logger:       logger,
		now:          time.Now,
	}
}

// Receive enforces the feedback-acceptance window via the embedded UUIDv7
// timestamp — no DB read on the 410 path. On the in-window path, delegates
// to the DAL's UpdateOutcome, which filters on (namespace, correlation_id,
// session_id) so cross-session attempts within the same namespace 404.
func (s *Service) Receive(ctx context.Context, namespace string, sessionID int64, correlationID uuid.UUID, outcome string, ttlMinutes int) (db.CorrelationRecord, error) {
	if correlationID.Version() != 7 {
		return db.CorrelationRecord{}, fmt.Errorf("%w: version %d", ErrInvalidCorrelationID, correlationID.Version())
	}

	sec, nsec := correlationID.Time().UnixTime()
	embedded := time.Unix(sec, nsec)
	if s.now().Sub(embedded) > time.Duration(ttlMinutes)*time.Minute {
		return db.CorrelationRecord{}, ErrFeedbackWindowExpired
	}

	return s.correlations.UpdateOutcome(ctx, namespace, sessionID, correlationID[:], outcome)
}
