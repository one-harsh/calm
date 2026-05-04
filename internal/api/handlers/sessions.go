// Copyright 2026 The CALM Authors
// SPDX-License-Identifier: Apache-2.0

package handlers

import (
	"net/http"

	"github.com/one-harsh/calm/internal/api/genapi"
)

func (h *Handlers) CreateSession(w http.ResponseWriter, r *http.Request) {
	h.notImplemented(w, r, "POST /v1/sessions")
}

func (h *Handlers) DeleteSession(w http.ResponseWriter, r *http.Request, _ genapi.SessionID) {
	h.notImplemented(w, r, "DELETE /v1/sessions/{session_id}")
}

func (h *Handlers) GetSnapshot(w http.ResponseWriter, r *http.Request, _ genapi.SessionID, _ genapi.GetSnapshotParams) {
	h.notImplemented(w, r, "GET /v1/sessions/{session_id}/snapshot")
}

func (h *Handlers) ListSources(w http.ResponseWriter, r *http.Request, _ genapi.SessionID) {
	h.notImplemented(w, r, "GET /v1/sessions/{session_id}/sources")
}
