// Copyright 2026 The CALM Authors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"flag"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	logging "github.com/one-harsh/context-logging"

	"github.com/one-harsh/calm/internal/api/genapi"
	"github.com/one-harsh/calm/internal/auth"
	"github.com/one-harsh/calm/internal/obs"
)

type adapterConfig struct {
	calmURL     string
	apiKey      string
	sessionTTL  int
	serviceName string
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "fatal:", err)
		os.Exit(1)
	}
}

func run() error {
	cfg := adapterConfig{
		serviceName: "calm-adapter",
	}
	flag.StringVar(&cfg.calmURL, "calm-url", "http://localhost:8080", "URL of the CALM service")
	flag.StringVar(&cfg.apiKey, "api-key", "", "API key for the CALM service (empty for local mode)")
	flag.IntVar(&cfg.sessionTTL, "session-ttl-minutes", 120, "session TTL in minutes")
	flag.Parse()

	logger, err := obs.NewLogger(cfg.serviceName, "dev", "local", "local", "info", "json")
	if err != nil {
		return err
	}
	defer func() { _ = logger.Sync() }()

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	logger.WithContext(ctx).Info(
		"adapter starting",
		logging.StringField("calm_url", cfg.calmURL),
	)

	client, err := genapi.NewClientWithResponses(cfg.calmURL, genapi.WithRequestEditorFn(apiKeyEditor(cfg.apiKey)))
	if err != nil {
		return fmt.Errorf("init client: %w", err)
	}

	if _, err := createSession(ctx, client, cfg.sessionTTL); err != nil {
		// never-worse invariant: never fail because CALM is unavailable. Log and continue.
		logger.WithContext(ctx).Warn(
			"create session failed; continuing without CALM",
			logging.ErrorField(err),
		)
	}

	logger.WithContext(ctx).Info("adapter ready (mcp loop not implemented yet)")
	<-ctx.Done()
	logger.WithContext(ctx).Info("adapter shutting down")
	return nil
}

func apiKeyEditor(apiKey string) genapi.RequestEditorFn {
	return func(_ context.Context, req *http.Request) error {
		if apiKey != "" {
			req.Header.Set(auth.HeaderAPIKey, apiKey)
		}
		return nil
	}
}

func createSession(ctx context.Context, client *genapi.ClientWithResponses, ttl int) (string, error) {
	reqCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()

	labels := map[string]string{"agent": "calm-adapter"}
	body := genapi.CreateSessionJSONRequestBody{
		Labels:     &labels,
		TtlMinutes: &ttl,
	}

	resp, err := client.CreateSessionWithResponse(reqCtx, &genapi.CreateSessionParams{}, body)
	if err != nil {
		return "", err
	}
	if resp.StatusCode() < 200 || resp.StatusCode() >= 300 {
		return "", fmt.Errorf("create session: %s", resp.Status())
	}
	if resp.JSON201 == nil {
		return "", fmt.Errorf("create session: unexpected empty body, status=%s", resp.Status())
	}
	return resp.JSON201.SessionToken, nil
}
