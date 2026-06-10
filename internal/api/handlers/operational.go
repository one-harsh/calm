// Copyright 2026 The CALM Authors
// SPDX-License-Identifier: Apache-2.0

package handlers

import (
	"context"

	logging "github.com/one-harsh/context-logging"

	"github.com/one-harsh/calm/internal/api/genapi"
)

func (h *Handlers) GetHealth(ctx context.Context, _ genapi.GetHealthRequestObject) (genapi.GetHealthResponseObject, error) {
	if err := h.deps.Health.Health(ctx); err != nil {
		h.deps.Logger.WithContext(ctx).Warn("health degraded", logging.ErrorField(err))
		return genapi.GetHealth503JSONResponse{
			Status: genapi.HealthResultStatusDegraded,
			Checks: map[string]genapi.HealthResultChecks{"postgres": genapi.Failed},
		}, nil
	}
	return genapi.GetHealth200JSONResponse{
		Status: genapi.HealthResultStatusOk,
		Checks: map[string]genapi.HealthResultChecks{"postgres": genapi.Ok},
	}, nil
}

func (h *Handlers) GetVersion(_ context.Context, _ genapi.GetVersionRequestObject) (genapi.GetVersionResponseObject, error) {
	return genapi.GetVersion200JSONResponse{Version: h.deps.Version}, nil
}
