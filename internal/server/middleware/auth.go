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

// Auth resolves the API key to a namespace. The key arrives as
// `Authorization: Bearer <key>`. Missing header, wrong scheme, or unknown
// key returns 401. No bypass path — the registry must contain the key.
func Auth(registry auth.Registry) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			raw := r.Header.Get(authHeader)
			// Case-sensitive scheme check: RFC 7235 says auth schemes are case-insensitive, but CALM accepts canonical "Bearer " only — loosening here = silent acceptance of client bugs.
			if !strings.HasPrefix(raw, bearerPrefix) {
				writeAuthFailure(w)
				return
			}
			ns, ok := registry.Resolve(strings.TrimPrefix(raw, bearerPrefix))
			if !ok {
				writeAuthFailure(w)
				return
			}

			ctx := logging.Bind(r.Context(), obs.Namespace(ns))
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

func writeAuthFailure(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusUnauthorized)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": "unauthorized"})
}
