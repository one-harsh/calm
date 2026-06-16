// Copyright 2026 The CALM Authors
// SPDX-License-Identifier: Apache-2.0

package server

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	logging "github.com/one-harsh/context-logging"

	"github.com/one-harsh/calm/internal/api"
	"github.com/one-harsh/calm/internal/api/genapi"
	"github.com/one-harsh/calm/internal/api/handlers"
	"github.com/one-harsh/calm/internal/auth"
	"github.com/one-harsh/calm/internal/server/middleware"
)

type Config struct {
	Address                  string
	MaxIngestPayloadKB       int
	RateLimitPerSecond       int
	RateLimitGlobalPerSecond int
	RateLimitPerIPPerSecond  int
	TrustProxyHeaders        bool
	RequestTimeout           time.Duration
	GracefulShutdownWait     time.Duration
	// TLSCert, when non-nil, makes the server terminate TLS itself
	// (server-auth). nil ⇒ plain HTTP (edge-terminated, the default).
	TLSCert *tls.Certificate
}

type Deps struct {
	Logger         *logging.Logger
	Registry       auth.Registry
	ClientResolver middleware.ClientResolver
	Sessions       middleware.SessionResolver
	Handlers       *handlers.Handlers
}

type Server struct {
	cfg  Config
	deps Deps
	srv  *http.Server
}

func NewHandler(cfg Config, deps Deps) (http.Handler, error) {
	spec, err := genapi.GetSpec()
	if err != nil {
		return nil, fmt.Errorf("load embedded openapi spec: %w", err)
	}
	sessionRoutes, err := middleware.ExtractSessionTokenRoutes(spec)
	if err != nil {
		return nil, fmt.Errorf("extract session-token routes: %w", err)
	}

	r := chi.NewRouter()

	// Chain order is canonical — reordering is HLD-touching.
	r.Use(middleware.Recovery(deps.Logger)) // outermost: records panics even when ctx was never hydrated
	r.Use(middleware.Context(deps.Logger))  // before Logging: every log line carries correlation/trace ids
	r.Use(middleware.Logging(deps.Logger))  // before Auth: failed-auth attempts are still logged with full context
	// After Logging (its 400 still emits a completion line); before RateLimitIP
	// (a malformed workload id is rejected without burning rate-limit tokens).
	r.Use(middleware.WorkloadRequestID())
	// Pre-auth: unauthenticated floods can't burn registry-lookup CPU.
	r.Use(middleware.RateLimitIP(cfg.RateLimitPerIPPerSecond, cfg.TrustProxyHeaders, deps.Logger))
	r.Use(middleware.Auth(deps.Registry, deps.ClientResolver, deps.Logger)) // stamps the namespace the next tier reads
	r.Use(middleware.RateLimitNamespaceAndGlobal(
		deps.Registry,
		cfg.RateLimitPerSecond,
		cfg.RateLimitGlobalPerSecond,
		deps.Logger,
	))
	// Before Timeout: rejecting an oversized body shouldn't consume the timeout budget.
	r.Use(middleware.BodySizeLimit(int64(cfg.MaxIngestPayloadKB) * 1024))
	r.Use(middleware.Timeout(cfg.RequestTimeout))
	r.Use(middleware.OpenAPIValidator(spec)) // before SessionResolve: missing-required-header fails before a DB lookup
	r.Use(middleware.SessionResolve(deps.Sessions, sessionRoutes, deps.Logger))

	api.Mount(r, deps.Handlers)
	return r, nil
}

func New(cfg Config, deps Deps) (*Server, error) {
	h, err := NewHandler(cfg, deps)
	if err != nil {
		return nil, err
	}
	srv := &http.Server{
		Addr:              cfg.Address,
		Handler:           h,
		ReadHeaderTimeout: 5 * time.Second,
	}
	if cfg.TLSCert != nil {
		srv.TLSConfig = &tls.Config{
			Certificates: []tls.Certificate{*cfg.TLSCert},
			MinVersion:   tls.VersionTLS12,
		}
	}
	return &Server{cfg: cfg, deps: deps, srv: srv}, nil
}

func (s *Server) Run(ctx context.Context) error {
	errCh := make(chan error, 1)
	go func() {
		s.deps.Logger.WithContext(ctx).Info(
			"http listening",
			logging.StringField("address", s.cfg.Address),
			logging.BoolField("tls", s.srv.TLSConfig != nil),
		)
		var err error
		if s.srv.TLSConfig != nil {
			err = s.srv.ListenAndServeTLS("", "")
		} else {
			err = s.srv.ListenAndServe()
		}
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
			return
		}
		errCh <- nil
	}()

	select {
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), s.cfg.GracefulShutdownWait)
		defer cancel()
		s.deps.Logger.WithContext(ctx).Info("http shutdown initiated")
		if err := s.srv.Shutdown(shutdownCtx); err != nil {
			return err
		}
		return nil
	case err := <-errCh:
		return err
	}
}
