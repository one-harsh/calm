// Copyright 2026 The CALM Authors
// SPDX-License-Identifier: Apache-2.0

package handlers

import (
	"net/http"

	"github.com/one-harsh/calm/internal/api/genapi"
)

func (h *Handlers) ManageListSessions(w http.ResponseWriter, r *http.Request, _ genapi.ManageListSessionsParams) {
	h.notImplemented(w, r, "GET /v1/manage/sessions")
}

func (h *Handlers) ManageDeleteSessions(w http.ResponseWriter, r *http.Request, _ genapi.ManageDeleteSessionsParams) {
	h.notImplemented(w, r, "DELETE /v1/manage/sessions")
}

func (h *Handlers) ManageListClients(w http.ResponseWriter, r *http.Request) {
	h.notImplemented(w, r, "GET /v1/manage/clients")
}

func (h *Handlers) ManageDeleteClient(w http.ResponseWriter, r *http.Request, _ string, _ genapi.ManageDeleteClientParams) {
	h.notImplemented(w, r, "DELETE /v1/manage/clients/{client}")
}
