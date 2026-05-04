// Copyright 2026 The CALM Authors
// SPDX-License-Identifier: Apache-2.0

package handlers

import "net/http"

func (h *Handlers) Ingest(w http.ResponseWriter, r *http.Request) {
	h.notImplemented(w, r, "POST /v1/ingest")
}
