// Copyright 2026 The CALM Authors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"

	logging "github.com/one-harsh/context-logging"

	"github.com/one-harsh/calm/internal/adapter/calm"
	"github.com/one-harsh/calm/internal/adapter/config"
	"github.com/one-harsh/calm/internal/adapter/mcp"
	"github.com/one-harsh/calm/internal/secrets"
)

const (
	serverName    = "calm-adapter"
	serverVersion = "dev"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "fatal:", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.Load(os.Getenv("CALM_ADAPTER_CONFIG_FILE"))
	if err != nil {
		return err
	}

	// stdout is the JSON-RPC protocol channel — logs must never touch it.
	logOut := io.Writer(os.Stderr)
	if cfg.Log.File != "" {
		f, err := os.OpenFile(cfg.Log.File, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
		if err != nil {
			return fmt.Errorf("open log file: %w", err)
		}
		defer func() { _ = f.Close() }()
		logOut = f
	}
	logger, err := logging.New(logging.Config{
		Level:       cfg.Log.Level,
		Format:      cfg.Log.Format,
		Output:      logOut,
		Service:     serverName,
		Version:     serverVersion,
		Environment: "local",
		Region:      "local",
	})
	if err != nil {
		return fmt.Errorf("init logger: %w", err)
	}
	logger = logger.WithCallerSkip(1)
	defer func() { _ = logger.Sync() }()

	// TODO: wire an OTel MeterProvider (Prometheus exporter, matching CALM's
	// own OTel surface) and thread a Meter through the MCP layer. The metrics
	// that make the Net context savings invariant operationally checkable
	// (adapter.response.visible_bytes, adapter.response.raw_bytes,
	// adapter.call.duration_ms, adapter.presentation.mode per DESIGN.md §7)
	// cannot fire until then.

	idempotencyKey, err := newIdempotencyKey()
	if err != nil {
		return fmt.Errorf("generate idempotency key: %w", err)
	}

	launchDir, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("determine launch directory: %w", err)
	}

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()
	ctx = logging.Bind(
		ctx,
		logging.StringField("calm_url", cfg.Calm.URL),
		logging.StringField("client", cfg.Calm.Client),
		logging.StringField("launch_dir", launchDir),
	)

	var apiKey string
	if cfg.Calm.APIKey != "" {
		apiKey = secrets.New(logger).ReadSecret(ctx, cfg.Calm.APIKey)
	}

	client, err := calm.NewGenapiClient(cfg.Calm.URL, apiKey, logger)
	if err != nil {
		return err
	}

	srv := mcp.NewServer(mcp.Config{
		Calm:                  client,
		Logger:                logger,
		ServerName:            serverName,
		ServerVersion:         serverVersion,
		DefaultClient:         cfg.Calm.Client,
		SessionTTLMinutes:     cfg.Calm.SessionTTLMinutes,
		LaunchDir:             launchDir,
		SessionIdempotencyKey: idempotencyKey,
	})

	logger.WithContext(ctx).Info("adapter starting")
	if err := srv.Serve(ctx, os.Stdin, os.Stdout); err != nil && !errors.Is(err, context.Canceled) {
		return err
	}
	logger.WithContext(ctx).Info("adapter stopped")
	return nil
}

// newIdempotencyKey returns a stable per-process key so a retried session create
// (e.g. a lost response after CALM committed) collapses to one session, not an orphan.
func newIdempotencyKey() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}
