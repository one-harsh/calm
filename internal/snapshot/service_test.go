// Copyright 2026 The CALM Authors
// SPDX-License-Identifier: Apache-2.0

package snapshot

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/mock"

	"github.com/one-harsh/calm/internal/db"
)

func evt(id int64, priority int, data string) db.Event {
	return db.Event{
		ID:        id,
		SessionID: 1,
		Type:      "tool_invocation",
		Priority:  priority,
		Data:      []byte(data),
		CreatedAt: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
	}
}

func serviceReturning(t *testing.T, events []db.Event, err error) *Service {
	t.Helper()
	repo := db.NewMockEventsRepo(t)
	repo.EXPECT().Snapshot(mock.Anything, "ns-a", int64(1)).Return(events, err).Once()
	dal := db.NewMockDAL(t)
	dal.EXPECT().Events().Return(repo).Once()
	return New(dal)
}

func TestBuild_AllFitWithinBudget(t *testing.T) {
	events := []db.Event{evt(1, 1, `{"a":1}`), evt(2, 2, `{"b":2}`)}
	svc := serviceReturning(t, events, nil)

	res, err := svc.Build(context.Background(), "ns-a", 1, 100_000)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if len(res.Events) != 2 {
		t.Errorf("included %d events; want 2", len(res.Events))
	}
	if res.BudgetExceeded {
		t.Error("BudgetExceeded = true; want false")
	}
	s0, _ := wireSize(events[0])
	s1, _ := wireSize(events[1])
	if res.ByteBudgetUsed != s0+s1 {
		t.Errorf("ByteBudgetUsed = %d; want %d", res.ByteBudgetUsed, s0+s1)
	}
}

func TestBuild_TruncatesWhenBudgetFills(t *testing.T) {
	events := []db.Event{evt(1, 1, `{"a":1}`), evt(2, 2, `{"b":2}`), evt(3, 3, `{"c":3}`)}
	s0, _ := wireSize(events[0])
	s1, _ := wireSize(events[1])
	svc := serviceReturning(t, events, nil)

	res, err := svc.Build(context.Background(), "ns-a", 1, s0+s1)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if len(res.Events) != 2 || res.Events[0].ID != 1 || res.Events[1].ID != 2 {
		t.Errorf("included = %+v; want events 1,2 in order", res.Events)
	}
	if !res.BudgetExceeded {
		t.Error("BudgetExceeded = false; want true")
	}
	if res.ByteBudgetUsed != s0+s1 {
		t.Errorf("ByteBudgetUsed = %d; want %d", res.ByteBudgetUsed, s0+s1)
	}
}

func TestBuild_P1OvershootCarveOut(t *testing.T) {
	events := []db.Event{evt(1, 1, `{"big":"payload"}`)}
	size, _ := wireSize(events[0])
	svc := serviceReturning(t, events, nil)

	res, err := svc.Build(context.Background(), "ns-a", 1, size-1)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if len(res.Events) != 1 || res.Events[0].ID != 1 {
		t.Errorf("included = %+v; want the single P1 event", res.Events)
	}
	if !res.BudgetExceeded {
		t.Error("BudgetExceeded = false; want true")
	}
	if res.ByteBudgetUsed <= size-1 {
		t.Errorf("ByteBudgetUsed = %d; want overshoot above budget %d", res.ByteBudgetUsed, size-1)
	}
}

func TestBuild_NoP1EmptyWhenTopDoesNotFit(t *testing.T) {
	events := []db.Event{evt(1, 2, `{"x":"y"}`)}
	size, _ := wireSize(events[0])
	svc := serviceReturning(t, events, nil)

	res, err := svc.Build(context.Background(), "ns-a", 1, size-1)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if len(res.Events) != 0 {
		t.Errorf("included %d events; want 0", len(res.Events))
	}
	if !res.BudgetExceeded {
		t.Error("BudgetExceeded = false; want true")
	}
	if res.ByteBudgetUsed != 0 {
		t.Errorf("ByteBudgetUsed = %d; want 0", res.ByteBudgetUsed)
	}
}

func TestBuild_NonPositiveBudgetUsesDefault(t *testing.T) {
	events := []db.Event{evt(1, 1, `{"a":1}`)}
	svc := serviceReturning(t, events, nil)

	res, err := svc.Build(context.Background(), "ns-a", 1, 0)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if len(res.Events) != 1 {
		t.Errorf("included %d events; want 1 (default budget applied)", len(res.Events))
	}
	if res.BudgetExceeded {
		t.Error("BudgetExceeded = true; want false")
	}
}

func TestBuild_PropagatesDALError(t *testing.T) {
	svc := serviceReturning(t, nil, db.ErrSessionNotFound)

	_, err := svc.Build(context.Background(), "ns-a", 1, 2048)
	if !errors.Is(err, db.ErrSessionNotFound) {
		t.Fatalf("err = %v; want ErrSessionNotFound", err)
	}
}
