// Copyright 2026 The CALM Authors
// SPDX-License-Identifier: Apache-2.0

package handlers

import (
	"context"

	"github.com/one-harsh/calm/internal/api/genapi"
)

func (h *Handlers) ManageListSessions(_ context.Context, _ genapi.ManageListSessionsRequestObject) (genapi.ManageListSessionsResponseObject, error) {
	return nil, ErrNotImplemented
}

func (h *Handlers) ManageDeleteSessions(_ context.Context, _ genapi.ManageDeleteSessionsRequestObject) (genapi.ManageDeleteSessionsResponseObject, error) {
	return nil, ErrNotImplemented
}

func (h *Handlers) ManageListClients(_ context.Context, _ genapi.ManageListClientsRequestObject) (genapi.ManageListClientsResponseObject, error) {
	return nil, ErrNotImplemented
}

func (h *Handlers) ManageDeleteClient(_ context.Context, _ genapi.ManageDeleteClientRequestObject) (genapi.ManageDeleteClientResponseObject, error) {
	return nil, ErrNotImplemented
}
