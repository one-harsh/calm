// Copyright 2026 The CALM Authors
// SPDX-License-Identifier: Apache-2.0

package feedback

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	logging "github.com/one-harsh/context-logging"

	"github.com/one-harsh/calm/internal/db"
)

type stubCorrelations struct{}

func (s *stubCorrelations) UpdateOutcome(ctx context.Context, namespace string, sessionID int64, correlationID []byte, outcome string) (db.CorrelationRecord, error) {
	return db.CorrelationRecord{}, nil
}

func uuidV7AtTime(at time.Time) uuid.UUID {
	id := uuid.UUID{}
	ms := at.UnixMilli()
	id[0] = byte(ms >> 40)
	id[1] = byte(ms >> 32)
	id[2] = byte(ms >> 24)
	id[3] = byte(ms >> 16)
	id[4] = byte(ms >> 8)
	id[5] = byte(ms)
	id[6] = 0x70
	id[8] = 0x80
	return id
}

func TestReceive_OutsideWindowReturnsErrFeedbackWindowExpired(t *testing.T) {
	svc := New(&stubCorrelations{}, logging.Nop())

	stale := uuidV7AtTime(time.Now().Add(-2 * time.Hour))

	_, err := svc.Receive(context.Background(), "ns-a", 1, stale, "success", 60)
	if err != ErrFeedbackWindowExpired {
		t.Errorf("err = %v; want ErrFeedbackWindowExpired", err)
	}
}
