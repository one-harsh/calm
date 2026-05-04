// Copyright 2026 The CALM Authors
// SPDX-License-Identifier: Apache-2.0

package api

import (
	"github.com/go-chi/chi/v5"

	"github.com/one-harsh/calm/internal/api/genapi"
	"github.com/one-harsh/calm/internal/api/handlers"
)

// Mount wires the generated chi-aware router to the supplied handlers. The
// route table itself lives in the generated code (driven by docs/api/openapi.yaml).
func Mount(r chi.Router, h *handlers.Handlers) {
	genapi.HandlerFromMux(h, r)
}
