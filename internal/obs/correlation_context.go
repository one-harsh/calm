// Copyright 2026 The CALM Authors
// SPDX-License-Identifier: Apache-2.0

package obs

import (
	"context"

	"github.com/google/uuid"
)

type correlationIDCtxKey struct{}

func WithCorrelationID(ctx context.Context, id uuid.UUID) context.Context {
	return context.WithValue(ctx, correlationIDCtxKey{}, id)
}

func CorrelationIDFromContext(ctx context.Context) (uuid.UUID, bool) {
	id, ok := ctx.Value(correlationIDCtxKey{}).(uuid.UUID)
	return id, ok
}
