// Copyright 2026 The CALM Authors
// SPDX-License-Identifier: Apache-2.0

package handlers

import (
	"encoding/json"
	"net/http"

	"github.com/one-harsh/calm/internal/obs"
)

const notImplementedMsg = "endpoint not implemented yet"

func (h *Handlers) notImplemented(w http.ResponseWriter, r *http.Request, endpoint string) {
	h.deps.Logger.WithContext(r.Context()).Warn(
		"endpoint not implemented",
		obs.Endpoint(endpoint),
	)
	respondJSON(w, http.StatusNotImplemented, map[string]string{
		"error":    notImplementedMsg,
		"endpoint": endpoint,
	})
}

func respondJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}
