// Copyright 2026 The CALM Authors
// SPDX-License-Identifier: Apache-2.0

package handlers

import (
	"context"
	"errors"
	"net/http"

	"github.com/one-harsh/calm/internal/db"
)

type errorMapping struct {
	Status int
	Code   string
	Detail string
}

func mapSessionError(err error) (m errorMapping, ok bool) {
	switch {
	case errors.Is(err, db.ErrSessionExists):
		return errorMapping{http.StatusConflict, "session_exists", "session already exists in this namespace"}, true
	case errors.Is(err, db.ErrSessionNotFound):
		return errorMapping{http.StatusNotFound, "session_not_found", "session not found in this namespace"}, true
	case errors.Is(err, db.ErrSessionTokenHashRequired):
		return errorMapping{http.StatusBadRequest, "invalid_request", "session token is required"}, true
	case errors.Is(err, db.ErrInvalidTTL):
		return errorMapping{http.StatusBadRequest, "invalid_request", "ttl_minutes must be a positive integer"}, true
	case errors.Is(err, db.ErrNamespaceRequired):
		return errorMapping{http.StatusBadRequest, "invalid_request", "namespace is required"}, true
	case errors.Is(err, db.ErrClientNotFound):
		return errorMapping{http.StatusBadRequest, "client_not_found", "client must be registered before sessions can reference it"}, true
	default:
		return errorMapping{}, false
	}
}

func mapClientError(err error) (m errorMapping, ok bool) {
	switch {
	case errors.Is(err, db.ErrClientExists):
		return errorMapping{http.StatusConflict, "client_exists", "client already registered in this namespace"}, true
	case errors.Is(err, db.ErrClientNotFound):
		return errorMapping{http.StatusNotFound, "client_not_found", "client not found in this namespace"}, true
	case errors.Is(err, db.ErrClientNameRequired):
		return errorMapping{http.StatusBadRequest, "invalid_request", "client name is required"}, true
	case errors.Is(err, db.ErrInvalidClientCredential):
		return errorMapping{http.StatusUnauthorized, "invalid_credential", "invalid client credential"}, true
	case errors.Is(err, db.ErrNamespaceRequired):
		return errorMapping{http.StatusBadRequest, "invalid_request", "namespace is required"}, true
	default:
		return errorMapping{}, false
	}
}

func mapEventsError(err error) (m errorMapping, ok bool) {
	switch {
	case errors.Is(err, db.ErrSessionNotFound):
		return errorMapping{http.StatusNotFound, "session_not_found", "session not found in this namespace"}, true
	case errors.Is(err, db.ErrInvalidPriority):
		return errorMapping{http.StatusBadRequest, "invalid_priority", "event priority must be in [1,4]"}, true
	case errors.Is(err, db.ErrInvalidLimit):
		return errorMapping{http.StatusBadRequest, "invalid_request", "limit must be a positive integer"}, true
	case errors.Is(err, db.ErrNamespaceRequired):
		return errorMapping{http.StatusBadRequest, "invalid_request", "namespace is required"}, true
	default:
		return errorMapping{}, false
	}
}

func isContextError(err error) bool {
	return errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)
}
