// Copyright 2026 The CALM Authors
// SPDX-License-Identifier: Apache-2.0

package handlers

import (
	"context"

	"github.com/one-harsh/calm/internal/api/genapi"
)

func (h *Handlers) WriteEvents(_ context.Context, _ genapi.WriteEventsRequestObject) (genapi.WriteEventsResponseObject, error) {
	return nil, ErrNotImplemented
}

func (h *Handlers) ReadEvents(_ context.Context, _ genapi.ReadEventsRequestObject) (genapi.ReadEventsResponseObject, error) {
	return nil, ErrNotImplemented
}
