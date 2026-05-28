// Copyright 2026 The CALM Authors
// SPDX-License-Identifier: Apache-2.0

package handlers

import (
	"context"

	"github.com/one-harsh/calm/internal/api/genapi"
)

func (h *Handlers) GetHealth(_ context.Context, _ genapi.GetHealthRequestObject) (genapi.GetHealthResponseObject, error) {
	return nil, ErrNotImplemented
}

func (h *Handlers) GetVersion(_ context.Context, _ genapi.GetVersionRequestObject) (genapi.GetVersionResponseObject, error) {
	return nil, ErrNotImplemented
}
