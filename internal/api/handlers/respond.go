// Copyright 2026 The CALM Authors
// SPDX-License-Identifier: Apache-2.0

package handlers

import (
	"encoding/json"
	"errors"
	"net/http"

	logging "github.com/one-harsh/context-logging"

	"github.com/one-harsh/calm/internal/api/genapi"
	"github.com/one-harsh/calm/internal/obs"
)

func (h *Handlers) StrictErrorHandler(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, ErrNotImplemented):
		writeError(w, http.StatusNotImplemented, genapi.Error{
			Error:    "not_implemented",
			Detail:   ptr("endpoint not implemented yet"),
			Endpoint: ptr(r.URL.Path),
		})
	default:
		h.deps.Logger.WithContext(r.Context()).Error(
			"handler returned unexpected error",
			obs.Endpoint(r.URL.Path),
			logging.ErrorField(err),
		)
		writeError(w, http.StatusInternalServerError, genapi.Error{
			Error:  "internal_error",
			Detail: ptr("unexpected error"),
		})
	}
}

func writeError(w http.ResponseWriter, status int, body genapi.Error) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

func ptr[T any](v T) *T { return &v }
