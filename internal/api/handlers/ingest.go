// Copyright 2026 The CALM Authors
// SPDX-License-Identifier: Apache-2.0

package handlers

import (
	"context"

	"github.com/one-harsh/calm/internal/api/genapi"
)

func (h *Handlers) Ingest(_ context.Context, _ genapi.IngestRequestObject) (genapi.IngestResponseObject, error) {
	return nil, ErrNotImplemented
}
