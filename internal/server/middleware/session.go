// Copyright 2026 The CALM Authors
// SPDX-License-Identifier: Apache-2.0

package middleware

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/getkin/kin-openapi/openapi3"
	logging "github.com/one-harsh/context-logging"

	"github.com/one-harsh/calm/internal/auth"
	"github.com/one-harsh/calm/internal/db"
	"github.com/one-harsh/calm/internal/obs"
	"github.com/one-harsh/calm/internal/session"
)

type SessionTokenRoutes map[string]bool

func (s SessionTokenRoutes) Has(method, path string) bool {
	return s[method+" "+path]
}

// ExtractSessionTokenRoutes fails on path-param routes — exact-match only.
func ExtractSessionTokenRoutes(spec *openapi3.T) (SessionTokenRoutes, error) {
	routes := SessionTokenRoutes{}
	if spec == nil || spec.Paths == nil {
		return routes, nil
	}
	for path, item := range spec.Paths.Map() {
		for method, op := range item.Operations() {
			if !hasSessionTokenParam(op.Parameters) && !hasSessionTokenParam(item.Parameters) {
				continue
			}
			if strings.Contains(path, "{") {
				return nil, fmt.Errorf("session-keyed route %s %s has a path parameter; extend ExtractSessionTokenRoutes to compile patterns", method, path)
			}
			routes[method+" "+path] = true
		}
	}
	return routes, nil
}

func hasSessionTokenParam(params openapi3.Parameters) bool {
	for _, p := range params {
		if p.Value == nil {
			continue
		}
		if p.Value.In == "header" && p.Value.Name == auth.HeaderSessionToken {
			return true
		}
	}
	return false
}

type SessionResolver interface {
	Lookup(ctx context.Context, namespace, sessionToken string) (session.SessionMetadata, error)
	Touch(ctx context.Context, namespace, sessionToken string, lastActivity time.Time) error
}

// SessionResolve scopes to routes whose OpenAPI declares X-CALM-Session-Token,
// resolves the token, then Touches on non-5xx. Touch failures don't change the response.
func SessionResolve(svc SessionResolver, routes SessionTokenRoutes, logger *logging.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if !routes.Has(r.Method, r.URL.Path) {
				next.ServeHTTP(w, r)
				return
			}
			token := r.Header.Get(auth.HeaderSessionToken)

			ctx := r.Context()
			namespace := auth.NamespaceFromContext(ctx)
			if namespace == "" {
				logger.WithContext(ctx).Error("session resolve: namespace missing from context")
				writeSessionNotFound(w)
				return
			}

			md, err := svc.Lookup(ctx, namespace, token)
			if err != nil {
				if errors.Is(err, db.ErrSessionNotFound) {
					writeSessionNotFound(w)
					return
				}
				logger.WithContext(ctx).Error("session resolve: lookup failed",
					logging.ErrorField(err),
				)
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusInternalServerError)
				_ = json.NewEncoder(w).Encode(map[string]string{"error": "internal_error"})
				return
			}

			ctx = session.WithMetadata(ctx, md)
			ctx = logging.Bind(ctx, obs.SessionID(md.ID))
			if md.Client != "" {
				ctx = logging.Bind(ctx, obs.Client(md.Client))
			}

			ww := &statusCapturingWriter{ResponseWriter: w}
			next.ServeHTTP(ww, r.WithContext(ctx))

			// HLD: every interaction Touches. 4xx is interaction; 5xx isn't.
			if ww.statusCode < 500 {
				if err := svc.Touch(ctx, namespace, token, time.Now().UTC()); err != nil {
					// Expected after DELETE /v1/sessions.
					if !errors.Is(err, db.ErrSessionNotFound) {
						logger.WithContext(ctx).Warn("session resolve: touch failed after handler",
							logging.ErrorField(err),
						)
					}
				}
			}
		})
	}
}

func writeSessionNotFound(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusNotFound)
	_ = json.NewEncoder(w).Encode(map[string]string{
		"error":  "session_not_found",
		"detail": "session not found in this namespace",
	})
}

type statusCapturingWriter struct {
	http.ResponseWriter
	statusCode int
	written    bool
}

func (w *statusCapturingWriter) WriteHeader(code int) {
	if !w.written {
		w.statusCode = code
		w.written = true
	}
	w.ResponseWriter.WriteHeader(code)
}

func (w *statusCapturingWriter) Write(b []byte) (int, error) {
	if !w.written {
		w.statusCode = http.StatusOK
		w.written = true
	}
	return w.ResponseWriter.Write(b)
}
