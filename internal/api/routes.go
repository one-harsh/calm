// Copyright 2026 The CALM Authors
// SPDX-License-Identifier: Apache-2.0

package api

import (
	"github.com/go-chi/chi/v5"

	"github.com/one-harsh/calm/internal/api/genapi"
	"github.com/one-harsh/calm/internal/api/handlers"
)

func Mount(r chi.Router, h *handlers.Handlers) {
	strictHandler := genapi.NewStrictHandlerWithOptions(h, nil, genapi.StrictHTTPServerOptions{
		RequestErrorHandlerFunc:  h.StrictErrorHandler,
		ResponseErrorHandlerFunc: h.StrictErrorHandler,
	})
	genapi.HandlerFromMux(strictHandler, r)
}
