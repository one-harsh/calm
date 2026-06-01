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

// OpenAPIValidator validates requests against the embedded spec. The
// authentication hook is suppressed — Auth middleware runs upstream and
// owns key resolution; otherwise kin-openapi would 401 every secured op.
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
