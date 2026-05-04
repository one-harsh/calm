// Copyright 2026 The CALM Authors
// SPDX-License-Identifier: Apache-2.0

package handlers

import (
	"net/http"

	"github.com/one-harsh/calm/internal/api/genapi"
)

func (h *Handlers) WriteEvents(w http.ResponseWriter, r *http.Request) {
	h.notImplemented(w, r, "POST /v1/events")
}

func (h *Handlers) ReadEvents(w http.ResponseWriter, r *http.Request, _ genapi.SessionID, _ genapi.ReadEventsParams) {
	h.notImplemented(w, r, "GET /v1/events/{session_id}")
}
