// Copyright 2026 The CALM Authors
// SPDX-License-Identifier: Apache-2.0

package middleware

import (
	"encoding/json"
	"net/http"
	"strings"

	logging "github.com/one-harsh/context-logging"

	"github.com/one-harsh/calm/internal/auth"
	"github.com/one-harsh/calm/internal/obs"
)

const (
	authHeader   = "Authorization"
	bearerPrefix = "Bearer "
)

// Auth resolves the API key to a namespace per HLD §6. The key arrives as
// `Authorization: Bearer <key>`. In local mode (empty registry) enforcement
// is skipped and the request is stamped with auth.LocalNamespace.
func Auth(registry auth.Registry) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ctx := r.Context()

			if registry.IsLocalMode() {
				ctx = logging.Bind(ctx, obs.Namespace(auth.LocalNamespace))
				next.ServeHTTP(w, r.WithContext(ctx))
				return
			}

			raw := r.Header.Get(authHeader)
			if !strings.HasPrefix(raw, bearerPrefix) {
				writeAuthFailure(w)
				return
			}
			ns, ok := registry.Resolve(strings.TrimPrefix(raw, bearerPrefix))
			if !ok {
				writeAuthFailure(w)
				return
			}

			ctx = logging.Bind(ctx, obs.Namespace(ns))
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

func writeAuthFailure(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusUnauthorized)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": "unauthorized"})
}
