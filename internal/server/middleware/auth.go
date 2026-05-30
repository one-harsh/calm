// Copyright 2026 The CALM Authors
// SPDX-License-Identifier: Apache-2.0

package middleware

import (
	"context"
	"encoding/json"
	"net/http"
	"path"
	"strings"

	logging "github.com/one-harsh/context-logging"

	"github.com/one-harsh/calm/internal/auth"
	"github.com/one-harsh/calm/internal/obs"
)

// Paths declared with security: [] in docs/api/openapi.yaml.
var unauthenticatedPaths = map[string]bool{
	"/v1/health":  true,
	"/v1/version": true,
}

type ClientResolver interface {
	ResolveByToken(ctx context.Context, namespace, rawToken string) (string, error)
}

func Auth(registry auth.Registry, clients ClientResolver, logger *logging.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if unauthenticatedPaths[r.URL.Path] {
				next.ServeHTTP(w, r)
				return
			}

			apiKey := r.Header.Get(auth.HeaderAPIKey)
			if apiKey == "" {
				writeAuthFailure(r.Context(), w, logger, "missing_api_key")
				return
			}
			ns, ok := registry.Resolve(apiKey)
			if !ok {
				writeAuthFailure(r.Context(), w, logger, "unknown_api_key")
				return
			}
			ctx := auth.WithNamespace(r.Context(), ns)
			ctx = logging.Bind(ctx, obs.Namespace(ns))

			// Chicken-and-egg: registration itself can't require a token.
			if registry.RequiresClientCredentials(ns) && !isClientRegistration(r.Method, r.URL.Path) {
				header := r.Header.Get(auth.HeaderAuthorization)
				if header == "" {
					writeAuthFailure(ctx, w, logger, "missing_client_token")
					return
				}
				if !strings.HasPrefix(header, auth.BearerPrefix) {
					writeAuthFailure(ctx, w, logger, "non_bearer_scheme")
					return
				}
				rawToken := strings.TrimPrefix(header, auth.BearerPrefix)
				if rawToken == "" {
					writeAuthFailure(ctx, w, logger, "empty_client_token")
					return
				}
				clientName, err := clients.ResolveByToken(ctx, ns, rawToken)
				if err != nil {
					writeAuthFailure(ctx, w, logger, "invalid_client_token")
					return
				}
				ctx = auth.WithClient(ctx, clientName)
				ctx = logging.Bind(ctx, obs.Client(clientName))
			}

			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// Mirrors POST /v1/clients/{name} in docs/api/openapi.yaml.
func isClientRegistration(method, urlPath string) bool {
	if method != http.MethodPost {
		return false
	}
	cleaned := path.Clean(urlPath)
	name, ok := strings.CutPrefix(cleaned, "/v1/clients/")
	if !ok {
		return false
	}
	return name != "" && !strings.Contains(name, "/")
}

func writeAuthFailure(ctx context.Context, w http.ResponseWriter, logger *logging.Logger, reason string) {
	logger.WithContext(ctx).Debug("auth failure",
		logging.StringField("auth.failure_reason", reason),
	)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusUnauthorized)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": "unauthorized"})
}
