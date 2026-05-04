// Copyright 2026 The CALM Authors
// SPDX-License-Identifier: Apache-2.0

package server

import (
	"context"
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
	Address              string
	MaxIngestPayloadKB   int
	RateLimitPerSecond   int
	RequestTimeout       time.Duration
	GracefulShutdownWait time.Duration
}

type Deps struct {
	Logger   *logging.Logger
	Registry auth.Registry
	Handlers *handlers.Handlers
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

	r := chi.NewRouter()

	// Middleware order is canonical (CLAUDE.md / HLD §10–§11).
	r.Use(middleware.Recovery(deps.Logger))
	r.Use(middleware.Context())
	r.Use(middleware.Logging(deps.Logger))
	r.Use(middleware.Auth(deps.Registry))
	r.Use(middleware.RateLimit(cfg.RateLimitPerSecond))
	r.Use(middleware.BodySizeLimit(int64(cfg.MaxIngestPayloadKB) * 1024))
	r.Use(middleware.Timeout(cfg.RequestTimeout))
	r.Use(middleware.OpenAPIValidator(spec))

	api.Mount(r, deps.Handlers)
	return r, nil
}

func New(cfg Config, deps Deps) (*Server, error) {
	h, err := NewHandler(cfg, deps)
	if err != nil {
		return nil, err
	}
	return &Server{
		cfg:  cfg,
		deps: deps,
		srv: &http.Server{
			Addr:              cfg.Address,
			Handler:           h,
			ReadHeaderTimeout: 5 * time.Second,
		},
	}, nil
}

func (s *Server) Run(ctx context.Context) error {
	errCh := make(chan error, 1)
	go func() {
		s.deps.Logger.Background().Info(
			"http listening",
			logging.StringField("address", s.cfg.Address),
		)
		if err := s.srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
			return
		}
		errCh <- nil
	}()

	select {
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), s.cfg.GracefulShutdownWait)
		defer cancel()
		s.deps.Logger.Background().Info("http shutdown initiated")
		if err := s.srv.Shutdown(shutdownCtx); err != nil {
			return err
		}
		return nil
	case err := <-errCh:
		return err
	}
}
