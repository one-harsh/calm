// Copyright 2026 The CALM Authors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	logging "github.com/one-harsh/context-logging"

	"github.com/one-harsh/calm/internal/api/handlers"
	"github.com/one-harsh/calm/internal/auth"
	"github.com/one-harsh/calm/internal/clientreg"
	"github.com/one-harsh/calm/internal/config"
	"github.com/one-harsh/calm/internal/db"
	"github.com/one-harsh/calm/internal/feedback"
	"github.com/one-harsh/calm/internal/ingest"
	"github.com/one-harsh/calm/internal/obs"
	"github.com/one-harsh/calm/internal/search"
	"github.com/one-harsh/calm/internal/secrets"
	"github.com/one-harsh/calm/internal/server"
	"github.com/one-harsh/calm/internal/session"
	"github.com/one-harsh/calm/internal/snapshot"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "fatal:", err)
		os.Exit(1)
	}
}

func run() error {
	// Raw tokens live transiently in memory; a core dump would persist them to disk.
	if err := syscall.Setrlimit(syscall.RLIMIT_CORE, &syscall.Rlimit{Cur: 0, Max: 0}); err != nil {
		fmt.Fprintf(os.Stderr, "warn: setrlimit(RLIMIT_CORE, 0) failed: %v\n", err)
	}

	cfg, err := config.Load(os.Getenv("CALM_CONFIG_FILE"))
	if err != nil {
		return err
	}

	logger, err := obs.NewLogger(
		cfg.Service.ServiceName, cfg.Service.Version, cfg.Service.Environment, cfg.Service.Region,
		cfg.Observability.Logging.Level, cfg.Observability.Logging.Format,
	)
	if err != nil {
		return err
	}
	defer func() { _ = logger.Sync() }()
	obs.InitTracePropagation(cfg.Observability.OTel.Enabled)

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	logger.WithContext(ctx).Info("service starting")

	secretReader := secrets.New(logger)
	registry := auth.BuildRegistry(ctx, cfg.Namespaces, secretReader, logger)

	openCtx, openCancel := context.WithTimeout(ctx, 30*time.Second)
	defer openCancel()
	store, err := db.Open(openCtx, cfg.Storage.DSN, cfg.Storage.MigrateOnStartup, logger)
	if err != nil {
		return fmt.Errorf("open storage: %w", err)
	}
	defer func() { _ = store.Close() }()

	clientSvc := clientreg.New(store, logger)
	sessionSvc := session.New(store, session.Config{
		CacheSize:          cfg.Sessions.CacheSize,
		IdempotencyKeyTTL:  cfg.Sessions.IdempotencyKeyTTL,
		IdempotencyKeySize: cfg.Sessions.IdempotencyKeySize,
	})
	snapshotSvc := snapshot.New(store, logger)
	ingestSvc := ingest.New(store, logger)
	searchSvc := search.New(store, logger)
	feedbackSvc := feedback.New(store, logger)
	if err := clientSvc.SeedDefaults(openCtx, uncredentialedNamespaceNames(cfg.Namespaces)); err != nil {
		return fmt.Errorf("seed clients: %w", err)
	}

	srv, err := server.New(server.Config{
		Address:                  cfg.Server.Address,
		MaxIngestPayloadKB:       cfg.Server.MaxIngestPayloadKB,
		RateLimitPerSecond:       cfg.Server.RateLimitPerSecond,
		RateLimitGlobalPerSecond: cfg.Server.RateLimitGlobalPerSecond,
		RateLimitPerIPPerSecond:  cfg.Server.RateLimitPerIPPerSecond,
		TrustProxyHeaders:        cfg.Server.TrustProxyHeaders,
		RequestTimeout:           cfg.Server.RequestTimeout,
		GracefulShutdownWait:     cfg.Server.GracefulShutdownWait,
	}, server.Deps{
		Logger:         logger,
		Registry:       registry,
		ClientResolver: clientSvc,
		Sessions:       sessionSvc,
		Handlers: handlers.New(handlers.Deps{
			Logger:   logger,
			Registry: registry,
			Clients:  clientSvc,
			Sessions: sessionSvc,
			Events:   store.Events(),
			Snapshot: snapshotSvc,
			Ingest:   ingestSvc,
			Search:   searchSvc,
			Feedback: feedbackSvc,
			Cfg: handlers.HandlersConfig{
				DefaultTTLMinutes: cfg.Sessions.DefaultTTLMinutes,
				MaxTTLMinutes:     cfg.Sessions.MaxTTLMinutes,
			},
		}),
	})
	if err != nil {
		return err
	}

	scanner := session.NewScanner(sessionSvc, session.ScannerConfig{
		Interval: time.Duration(cfg.Sessions.TTLScannerIntervalMS) * time.Millisecond,
		Jitter:   time.Duration(cfg.Sessions.TTLScannerJitterMS) * time.Millisecond,
	}, logger)

	srvCtx, cancel := context.WithCancel(ctx)
	var wg sync.WaitGroup
	wg.Go(func() {
		if err := scanner.Run(srvCtx); err != nil {
			logger.WithContext(ctx).Error("ttl scanner exited with error",
				logging.ErrorField(err))
		}
	})

	srvErr := srv.Run(srvCtx)
	cancel()
	wg.Wait() // drain in-flight scanner iteration before returning

	if srvErr != nil && !errors.Is(srvErr, context.Canceled) {
		return srvErr
	}
	logger.WithContext(ctx).Info("service stopped")
	return nil
}

// uncredentialedNamespaceNames returns names of namespaces that auto-seed
// the default client. Credentialed namespaces (require_client_credentials=true)
// are skipped: the seeded `default` client would have no client token and
// would be unreachable from session operations there (the auth middleware
// requires a client token in credentialed namespaces), so seeding it is
// dead state.
func uncredentialedNamespaceNames(ns []config.NamespaceConfig) []string {
	out := make([]string, 0, len(ns))
	for _, n := range ns {
		if !n.RequireClientCredentials {
			out = append(out, n.Name)
		}
	}
	return out
}
