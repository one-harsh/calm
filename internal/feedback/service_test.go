// Copyright 2026 The CALM Authors
// SPDX-License-Identifier: Apache-2.0

package feedback

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	logging "github.com/one-harsh/context-logging"
	"github.com/stretchr/testify/mock"

	"github.com/one-harsh/calm/internal/db"
)

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

// newMockService returns a feedback service over a MockDAL whose WithTx
// invokes the closure with a MockCorrelationsRepo, so tests set Get +
// UpdateOutcome expectations directly.
func newMockService(t *testing.T) (*Service, *db.MockCorrelationsRepo) {
	t.Helper()
	corr := db.NewMockCorrelationsRepo(t)
	dal := db.NewMockDAL(t)
	dal.EXPECT().WithTx(mock.Anything, mock.Anything).RunAndReturn(
		func(_ context.Context, fn func(db.Repos) error) error {
			return fn(db.Repos{Correlations: corr})
		},
	).Maybe()
	return New(dal, logging.Nop()), corr
}

func TestReceive_OutsideWindowReturnsErrFeedbackWindowExpired(t *testing.T) {
	// No DB read on the 410 path; the mock fails the test if Receive reaches it.
	svc, _ := newMockService(t)

	stale := uuidV7AtTime(time.Now().Add(-2 * time.Hour))

	_, err := svc.Receive(context.Background(), "ns-a", 1, stale, "success", 60)
	if !errors.Is(err, ErrFeedbackWindowExpired) {
		t.Errorf("err = %v; want ErrFeedbackWindowExpired", err)
	}
}

func TestReceive_RejectsNonUUIDv7With400(t *testing.T) {
	svc, _ := newMockService(t)

	v4 := uuid.New()

	_, err := svc.Receive(context.Background(), "ns-a", 1, v4, "success", 60)
	if !errors.Is(err, ErrInvalidCorrelationID) {
		t.Errorf("err = %v; want ErrInvalidCorrelationID", err)
	}
}

func TestReceive_UnknownCorrelationReturns404Sentinel(t *testing.T) {
	svc, corr := newMockService(t)
	corr.EXPECT().GetWithLockedRow(mock.Anything, "ns-a", int64(1), mock.Anything).
		Return(db.CorrelationRecord{}, db.ErrCorrelationNotFound).Once()

	fresh, err := uuid.NewV7()
	if err != nil {
		t.Fatalf("uuid.NewV7: %v", err)
	}

	_, err = svc.Receive(context.Background(), "ns-a", 1, fresh, "success", 60)
	if !errors.Is(err, db.ErrCorrelationNotFound) {
		t.Errorf("err = %v; want ErrCorrelationNotFound", err)
	}
}

func TestReceive_AlreadySubmittedReturns409Sentinel(t *testing.T) {
	svc, corr := newMockService(t)
	corr.EXPECT().GetWithLockedRow(mock.Anything, "ns-a", int64(1), mock.Anything).
		Return(db.CorrelationRecord{FeedbackReceivedAt: time.Now()}, nil).Once()

	fresh, err := uuid.NewV7()
	if err != nil {
		t.Fatalf("uuid.NewV7: %v", err)
	}

	_, err = svc.Receive(context.Background(), "ns-a", 1, fresh, "success", 60)
	if !errors.Is(err, db.ErrFeedbackAlreadySubmitted) {
		t.Errorf("err = %v; want ErrFeedbackAlreadySubmitted", err)
	}
}

func TestReceive_HappyPathUpdatesAndReturnsRecord(t *testing.T) {
	svc, corr := newMockService(t)
	corr.EXPECT().GetWithLockedRow(mock.Anything, "ns-a", int64(1), mock.Anything).
		Return(db.CorrelationRecord{RequestType: "ingest"}, nil).Once()
	want := db.CorrelationRecord{
		Namespace:   "ns-a",
		SessionID:   1,
		RequestType: "ingest",
		Outcome:     "success",
	}
	corr.EXPECT().UpdateOutcome(mock.Anything, "ns-a", int64(1), mock.Anything, "success").
		Return(want, nil).Once()

	fresh, err := uuid.NewV7()
	if err != nil {
		t.Fatalf("uuid.NewV7: %v", err)
	}

	got, err := svc.Receive(context.Background(), "ns-a", 1, fresh, "success", 60)
	if err != nil {
		t.Fatalf("Receive: %v", err)
	}
	if got.Outcome != want.Outcome || got.RequestType != want.RequestType {
		t.Errorf("record = %+v; want %+v", got, want)
	}
}
