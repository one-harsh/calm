// Copyright 2026 The CALM Authors
// SPDX-License-Identifier: Apache-2.0

package middleware

import (
	"context"
	"fmt"
	"net/http"

	"github.com/getkin/kin-openapi/openapi3"
	"github.com/getkin/kin-openapi/openapi3filter"
	nethttpmiddleware "github.com/oapi-codegen/nethttp-middleware"
)

// OpenAPIValidator validates incoming requests against the embedded OpenAPI
// spec (path, query params, headers, body schema). Slot innermost, after
// Auth/RateLimit/BodySizeLimit/Timeout — request validation is part of the
// per-request work budget (HLD §11).
//
// kin-openapi's authentication check is intentionally suppressed here: API
// key resolution and namespace stamping are owned by middleware.Auth, which
// runs before this middleware. Without this hook, kin-openapi would reject
// every request whose operation declares the apiKey security scheme.
func OpenAPIValidator(spec *openapi3.T) func(http.Handler) http.Handler {
	spec.Servers = nil

	options := &nethttpmiddleware.Options{
		Options: openapi3filter.Options{
			AuthenticationFunc: func(_ context.Context, _ *openapi3filter.AuthenticationInput) error {
				return nil
			},
		},
		ErrorHandler: func(w http.ResponseWriter, message string, statusCode int) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(statusCode)
			_, _ = fmt.Fprintf(w, `{"error":%q}`, message)
		},
	}
	return nethttpmiddleware.OapiRequestValidatorWithOptions(spec, options)
}
