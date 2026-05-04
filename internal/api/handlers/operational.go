// Copyright 2026 The CALM Authors
// SPDX-License-Identifier: Apache-2.0

package handlers

import "net/http"

func (h *Handlers) GetHealth(w http.ResponseWriter, r *http.Request) {
	h.notImplemented(w, r, "GET /v1/health")
}

func (h *Handlers) GetVersion(w http.ResponseWriter, r *http.Request) {
	h.notImplemented(w, r, "GET /v1/version")
}
