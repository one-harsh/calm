// Copyright 2026 The CALM Authors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"crypto/tls"
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
	disableCoreDumps()

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
	store, err := db.Open(openCtx, db.OpenConfig{
		DSN:                        cfg.Storage.DSN,
		MigrateOnStartup:           cfg.Storage.MigrateOnStartup,
		MaxOpenConns:               cfg.Storage.MaxOpenConns,
		MaxIdleConns:               cfg.Storage.MaxIdleConns,
		ConnMaxLifetime:            cfg.Storage.ConnMaxLifetime,
		TrigramSimilarityThreshold: cfg.Storage.TrigramSimilarityThreshold,
	}, logger)
	if err != nil {
		return fmt.Errorf("open storage: %w", err)
	}
	defer func() { _ = store.Close() }()

	clientSvc := clientreg.New(store, logger)
	sessionSvc := session.New(store, session.Config{
		CacheSize:          cfg.Sessions.CacheSize,
		IdempotencyKeyTTL:  cfg.Sessions.IdempotencyKeyTTL,
		IdempotencyKeySize: cfg.Sessions.IdempotencyKeySize,
	}, logger)
	snapshotSvc := snapshot.New(store, logger)
	ingestSvc := ingest.New(store, logger)
	searchSvc := search.New(store, logger)
	feedbackSvc := feedback.New(store, logger)
	if err := clientSvc.SeedDefaults(openCtx, uncredentialedNamespaceNames(cfg.Namespaces)); err != nil {
		return fmt.Errorf("seed clients: %w", err)
	}

	tlsCert, err := loadTLSCert(ctx, cfg.Server.TLS, secretReader)
	if err != nil {
		return err
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
		TLSCert:                  tlsCert,
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
			Health:   store,
			Snapshot: snapshotSvc,
			Ingest:   ingestSvc,
			Search:   searchSvc,
			Feedback: feedbackSvc,
			Version:  cfg.Service.Version,
			Cfg: handlers.HandlersConfig{
				DefaultTTLMinutes:    cfg.Sessions.DefaultTTLMinutes,
				MaxTTLMinutes:        cfg.Sessions.MaxTTLMinutes,
				SearchMaxBudgetBytes: cfg.Search.MaxBudgetKB * 1024,
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

// loadTLSCert builds the server keypair when TLS is enabled; nil ⇒ plain HTTP
// (edge-terminated, the default). Missing/unreadable cert/key references Fatal
// via SecretReader; only an invalid PEM pair surfaces as a returned error.
func loadTLSCert(ctx context.Context, cfg config.TLSConfig, r secrets.SecretReader) (*tls.Certificate, error) {
	if !cfg.Enabled {
		return nil, nil
	}
	cert, err := tls.X509KeyPair(
		[]byte(r.ReadSecret(ctx, cfg.CertFile)),
		[]byte(r.ReadSecret(ctx, cfg.KeyFile)),
	)
	if err != nil {
		return nil, fmt.Errorf("server.tls: load keypair: %w", err)
	}
	return &cert, nil
}
