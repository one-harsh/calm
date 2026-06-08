// Copyright 2026 The CALM Authors
// SPDX-License-Identifier: Apache-2.0

package snapshot

import (
	"context"
	"encoding/json"
	"time"

	"github.com/google/uuid"
	logging "github.com/one-harsh/context-logging"

	"github.com/one-harsh/calm/internal/db"
	"github.com/one-harsh/calm/internal/obs"
)

// DefaultBudgetBytes is applied for a non-positive budget. Matches the API
// contract's default for GET /v1/snapshot.
const DefaultBudgetBytes = 2048

const requestTypeSnapshot = "snapshot"

type Service struct {
	store  db.DAL
	logger *logging.Logger
}

func New(store db.DAL, logger *logging.Logger) *Service {
	return &Service{store: store, logger: logger}
}

type Result struct {
	Events         []db.Event
	ByteBudgetUsed int
	BudgetExceeded bool
}

func (s *Service) Build(ctx context.Context, namespace string, sessionID int64, correlationID uuid.UUID, budgetBytes int) (Result, error) {
	if budgetBytes <= 0 {
		budgetBytes = DefaultBudgetBytes
	}

	events, err := s.store.Events().Snapshot(ctx, namespace, sessionID)
	if err != nil {
		return Result{}, err
	}

	included := make([]db.Event, 0, len(events))
	used := 0
	exceeded := false
	for _, ev := range events {
		size, err := wireSize(ev)
		if err != nil {
			return Result{}, err
		}
		if used+size <= budgetBytes {
			included = append(included, ev)
			used += size
			continue
		}
		// P1 survives "at all costs" (HLD State Reconstruction): events are
		// priority-asc, so a present P1 is first — include it even if it alone
		// overshoots the budget rather than return an empty snapshot.
		if len(included) == 0 && ev.Priority == 1 {
			included = append(included, ev)
			used += size
		}
		exceeded = true
		break
	}

	result := Result{Events: included, ByteBudgetUsed: used, BudgetExceeded: exceeded}
	s.captureCorrelation(ctx, namespace, sessionID, correlationID, result)
	return result, nil
}

func (s *Service) captureCorrelation(ctx context.Context, namespace string, sessionID int64, correlationID uuid.UUID, result Result) {
	meta, err := json.Marshal(map[string]any{
		"byte_budget_used": result.ByteBudgetUsed,
		"budget_exceeded":  result.BudgetExceeded,
		"event_count":      len(result.Events),
	})
	if err != nil {
		s.logger.WithContext(ctx).Warn("correlation marshal failed",
			obs.RequestType(requestTypeSnapshot), logging.ErrorField(err))
		return
	}
	if err := s.store.Correlations().Insert(ctx, namespace, sessionID, correlationID[:], requestTypeSnapshot, meta); err != nil {
		s.logger.WithContext(ctx).Warn("correlation insert failed",
			obs.RequestType(requestTypeSnapshot), logging.ErrorField(err))
	}
}

// wireSize is an event's serialized JSON size — the unit the byte budget
// governs. The budget is an event-payload budget: it sums standalone event
// sizes and counts neither the response envelope nor the array separators, so
// the actual HTTP body runs slightly larger than byte_budget_used.
func wireSize(ev db.Event) (int, error) {
	data := ev.Data
	if len(data) == 0 {
		data = []byte("{}")
	}
	b, err := json.Marshal(sizedEvent{
		ID:        ev.ID,
		Type:      ev.Type,
		Priority:  ev.Priority,
		Data:      json.RawMessage(data),
		CreatedAt: ev.CreatedAt,
	})
	if err != nil {
		return 0, err
	}
	return len(b), nil
}

type sizedEvent struct {
	ID        int64           `json:"id"`
	Type      string          `json:"type"`
	Priority  int             `json:"priority"`
	Data      json.RawMessage `json:"data"`
	CreatedAt time.Time       `json:"created_at"`
}
