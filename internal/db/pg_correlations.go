// Copyright 2026 The CALM Authors
// SPDX-License-Identifier: Apache-2.0

package db

import (
	"context"

	logging "github.com/one-harsh/context-logging"
)

type correlationsRepo struct {
	queryer queryer
	logger  *logging.Logger
}

func (r *correlationsRepo) UpdateOutcome(ctx context.Context, namespace string, sessionID int64, correlationID []byte, outcome string) (CorrelationRecord, error) {
	return CorrelationRecord{}, ErrCorrelationsNotImplemented
}
