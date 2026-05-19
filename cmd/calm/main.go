// Copyright 2026 The CALM Authors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/one-harsh/calm/internal/api/handlers"
	"github.com/one-harsh/calm/internal/auth"
	"github.com/one-harsh/calm/internal/config"
	"github.com/one-harsh/calm/internal/db"
	"github.com/one-harsh/calm/internal/obs"
	"github.com/one-harsh/calm/internal/server"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "fatal:", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}

	logger, err := obs.NewLogger(
		cfg.ServiceName, cfg.Version, cfg.Environment, cfg.Region,
		cfg.LogLevel, cfg.LogFormat,
	)
	if err != nil {
		return err
	}
	defer func() { _ = logger.Sync() }()

	logger.Background().Info("service starting")

	registry := auth.LoadRegistry(context.Background(), cfg.APIKeysFile, logger)

	openCtx, openCancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer openCancel()
	store, err := db.Open(openCtx, cfg.StorageDSN, logger)
	if err != nil {
		return fmt.Errorf("open storage: %w", err)
	}
	defer func() { _ = store.Close() }()

	srv, err := server.New(server.Config{
		Address:              cfg.Address,
		MaxIngestPayloadKB:   cfg.MaxIngestPayloadKB,
		RateLimitPerSecond:   cfg.RateLimitPerSecond,
		RequestTimeout:       2 * time.Second,
		GracefulShutdownWait: 10 * time.Second,
	}, server.Deps{
		Logger:   logger,
		Registry: registry,
		Handlers: handlers.New(handlers.Deps{Logger: logger, Store: store}),
	})
	if err != nil {
		return err
	}

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	if err := srv.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
		return err
	}
	logger.Background().Info("service stopped")
	return nil
}
