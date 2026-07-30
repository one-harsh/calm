// Copyright 2026 The CALM Authors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	logging "github.com/one-harsh/context-logging"

	"github.com/one-harsh/calm/internal/adapter/calm"
	"github.com/one-harsh/calm/internal/adapter/capturecli"
	"github.com/one-harsh/calm/internal/adapter/config"
	"github.com/one-harsh/calm/internal/secrets"
)

const (
	binName    = "calm-capture"
	binVersion = "dev"
)

func main() {
	os.Exit(run())
}

func run() int {
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	args := os.Args[1:]
	// nested under an ancestor's capture — pass through before any bootstrap
	// work (no config load, no log file, no client) on the hot path.
	if len(args) > 0 && args[0] == "exec" && os.Getenv(capturecli.CaptureActiveEnv) == "1" {
		return capturecli.PassthroughExec(ctx, args[1:], os.Stdout, os.Stderr)
	}

	d, cleanup, err := bootstrap()
	if err != nil {
		// never-worse: a broken CALM setup must never block the local action —
		// exec degrades to a pure passthrough of the wrapped command, and hook
		// to a stdin→stdout transform that rewrites/passes through with the
		// static card. The CALM-facing commands fail honestly instead.
		if len(args) > 0 && args[0] == "exec" {
			return capturecli.PassthroughExec(ctx, args[1:], os.Stdout, os.Stderr)
		}
		if len(args) > 0 && args[0] == "hook" {
			return capturecli.HookDegraded(ctx, os.Stdin, os.Stdout)
		}
		fmt.Fprintln(os.Stderr, "fatal:", err)
		return 2
	}
	defer cleanup()

	ctx = logging.Bind(ctx,
		logging.StringField("calm_url", d.Cfg.Calm.URL),
		logging.StringField("client", d.Cfg.Calm.Client))
	return capturecli.Dispatch(ctx, d, args)
}

func bootstrap() (capturecli.Deps, func(), error) {
	root, err := capturecli.ResolveRoot()
	if err != nil {
		return capturecli.Deps{}, nil, err
	}

	cfg, err := config.Load(os.Getenv("CALM_ADAPTER_CONFIG_FILE"), root)
	if err != nil {
		return capturecli.Deps{}, nil, err
	}

	logOut := logFileOrDiscard(cfg.Log.File, root)
	logger, err := logging.New(logging.Config{
		Level:       cfg.Log.Level,
		Format:      cfg.Log.Format,
		Output:      logOut,
		Service:     binName,
		Version:     binVersion,
		Environment: "local",
		Region:      "local",
	})
	if err != nil {
		return capturecli.Deps{}, nil, fmt.Errorf("init logger: %w", err)
	}
	logger = logger.WithCallerSkip(1)

	// secrets.Resolve, not ReadSecret: the reader's fail-fast Fatal would kill
	// the process before exec's never-worse floor could run the wrapped
	// command. The CLI's fail-earliest surface is `init` at install time.
	var apiKey string
	if cfg.Calm.APIKey != "" {
		apiKey, err = secrets.Resolve(cfg.Calm.APIKey)
		if err != nil {
			_ = logger.Sync()
			return capturecli.Deps{}, nil, err
		}
	}
	client, err := calm.NewGenapiClient(cfg.Calm.URL, apiKey, logger)
	if err != nil {
		_ = logger.Sync()
		return capturecli.Deps{}, nil, err
	}

	return capturecli.Deps{
		Cfg:    cfg,
		Logger: logger,
		Client: client,
		Root:   root,
		Stdin:  os.Stdin,
		Stdout: os.Stdout,
		Stderr: os.Stderr,
	}, func() { _ = logger.Sync() }, nil
}

func logFileOrDiscard(configured, root string) io.Writer {
	path := configured
	if path == "" {
		dir := filepath.Join(root, "logs")
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return io.Discard
		}
		path = filepath.Join(dir, "calm-capture.log")
	}
	//nolint:gosec // this is for log path
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return io.Discard
	}
	return f
}
