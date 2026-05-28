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
	"github.com/one-harsh/calm/internal/client"
	"github.com/one-harsh/calm/internal/config"
	"github.com/one-harsh/calm/internal/db"
	"github.com/one-harsh/calm/internal/obs"
	"github.com/one-harsh/calm/internal/secrets"
	"github.com/one-harsh/calm/internal/server"
	"github.com/one-harsh/calm/internal/session"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "fatal:", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.Load(os.Getenv("CALM_CONFIG_FILE"))
	if err != nil {
		return err
	}

	logger, err := obs.NewLogger(
		cfg.Service.ServiceName, cfg.Service.Version, cfg.Service.Environment, cfg.Service.Region,
		cfg.Service.LogLevel, cfg.Service.LogFormat,
	)
	if err != nil {
		return err
	}
	defer func() { _ = logger.Sync() }()

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

	clientSvc := client.New(store)
	sessionSvc := session.New(store, cfg.Sessions.CacheSize)
	if err := clientSvc.SeedDefaults(openCtx, namespaceNames(cfg.Namespaces)); err != nil {
		return fmt.Errorf("seed clients: %w", err)
	}

	srv, err := server.New(server.Config{
		Address:              cfg.Server.Address,
		MaxIngestPayloadKB:   cfg.Server.MaxIngestPayloadKB,
		RateLimitPerSecond:   cfg.Server.RateLimitPerSecond,
		RequestTimeout:       cfg.Server.RequestTimeout,
		GracefulShutdownWait: cfg.Server.GracefulShutdownWait,
	}, server.Deps{
		Logger:   logger,
		Registry: registry,
		Handlers: handlers.New(handlers.Deps{
			Logger:   logger,
			Clients:  clientSvc,
			Sessions: sessionSvc,
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

func namespaceNames(ns []config.NamespaceConfig) []string {
	out := make([]string, len(ns))
	for i, n := range ns {
		out[i] = n.Name
	}
	return out
}
