//go:build mocks

// Copyright 2026 The CALM Authors
// SPDX-License-Identifier: Apache-2.0

package handlers

import (
	"context"
	"errors"
	"testing"

	logging "github.com/one-harsh/context-logging"
	"github.com/stretchr/testify/mock"

	"github.com/one-harsh/calm/internal/api/genapi"
	"github.com/one-harsh/calm/internal/db"
)

func TestGetHealth_DegradedWhenBackendUnreachable(t *testing.T) {
	hc := db.NewMockHealthChecker(t)
	hc.EXPECT().Health(mock.Anything).Return(errors.New("dial tcp: connection refused")).Once()

	h := New(Deps{Logger: logging.Nop(), Health: hc})
	resp, err := h.GetHealth(context.Background(), genapi.GetHealthRequestObject{})
	if err != nil {
		t.Fatalf("GetHealth returned error: %v", err)
	}

	r, ok := resp.(genapi.GetHealth503JSONResponse)
	if !ok {
		t.Fatalf("expected GetHealth503JSONResponse, got %T", resp)
	}
	if r.Status != genapi.HealthResultStatusDegraded {
		t.Errorf("status = %q; want degraded", r.Status)
	}
	if r.Checks["postgres"] != genapi.Failed {
		t.Errorf("checks[postgres] = %q; want failed", r.Checks["postgres"])
	}
}
