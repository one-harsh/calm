// Copyright 2026 The CALM Authors
// SPDX-License-Identifier: Apache-2.0

package handlers

import (
	"context"

	"github.com/google/uuid"
	logging "github.com/one-harsh/context-logging"

	"github.com/one-harsh/calm/internal/obs"
)

func correlationIDOrNil(ctx context.Context, logger *logging.Logger, requestType string) uuid.UUID {
	if id, ok := obs.CorrelationIDFromContext(ctx); ok {
		return id
	}
	logger.WithContext(ctx).Warn(
		"correlation id missing from context; request proceeds without capture",
		obs.RequestType(requestType),
	)
	return uuid.Nil
}
