// Copyright 2026 The CALM Authors
// SPDX-License-Identifier: Apache-2.0

package capturecli

import (
	"context"
	"fmt"
	"io"
	"math/rand/v2"
	"os"
	"path/filepath"
	"time"

	logging "github.com/one-harsh/context-logging"

	"github.com/one-harsh/calm/internal/adapter/calm"
	"github.com/one-harsh/calm/internal/adapter/capture"
	"github.com/one-harsh/calm/internal/adapter/config"
	"github.com/one-harsh/calm/internal/adapter/obs"
	"github.com/one-harsh/calm/internal/adapter/session"
)

const (
	CaptureActiveEnv string = "CALM_CAPTURE_ACTIVE"
	binaryName       string = "calm-capture"
	defaultSessionID string = "default"

	opTimeout = 10 * time.Second
)

type Deps struct {
	Cfg    config.Config
	Logger *logging.Logger
	Client calm.Client
	Root   string
	Stdin  io.Reader
	Stdout io.Writer
	Stderr io.Writer
}

func Dispatch(ctx context.Context, d Deps, args []string) int {
	if len(args) == 0 {
		_, _ = fmt.Fprintln(d.Stderr, "usage: calm-capture <exec|search|feedback|hook|init> [flags]")
		return 2
	}
	switch args[0] {
	case "exec":
		return d.execCmd(ctx, args[1:])
	case "search":
		return d.searchCmd(ctx, args[1:])
	case "feedback":
		return d.feedbackCmd(ctx, args[1:])
	case "hook":
		return d.hookCmd(ctx)
	case "init":
		return d.initCmd(ctx, args[1:])
	default:
		_, _ = fmt.Fprintf(d.Stderr, "calm-capture: unknown command %q\n", args[0])
		return 2
	}
}

func ResolveRoot() (string, error) {
	if h := os.Getenv("CALM_HOME"); h != "" {
		return h, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".calm"), nil
}

func (d Deps) manager(sessionID string) (*session.Manager, error) {
	return session.New(session.Config{
		SessionID:  sessionID,
		Client:     d.Cfg.Calm.Client,
		CALM:       d.Client,
		Logger:     d.Logger,
		TTLMinutes: d.Cfg.Calm.SessionTTLMinutes,
		RootDir:    d.Root,
	})
}

func sessionIDOr(v string) string {
	if v == "" {
		return defaultSessionID
	}
	return v
}

// A bare command is off PATH from the model's seat and resolves the wrong session.
func recallFor(binPath, sessionID string) string {
	return shellSingleQuote(binPath) + " search --session " + shellSingleQuote(sessionID)
}

func withCallSummary(ctx context.Context) context.Context {
	ctx, reqID := obs.WithCallContext(ctx)
	ctx = logging.Bind(
		ctx,
		obs.WorkloadRequestID(reqID),
		logging.BoolField(obs.KeyCaptured, false),
		logging.BoolField(obs.KeyDegraded, false),
	)
	return logging.BindSummary(
		ctx,
		obs.PresentationModeFieldSummary,
		obs.ResponseVisibleBytes(0),
		obs.ResponseRawBytes(0),
	)
}

func bindDegraded(ctx context.Context, reason string) {
	logging.BindSummary(
		ctx,
		logging.BoolField(obs.KeyDegraded, true),
		obs.DegradedReasonField(reason),
	)
}

func (d Deps) degradedStderr(ctx context.Context, reason string) int {
	n, _ := fmt.Fprintln(d.Stderr, obs.DegradedPhrase(reason))
	bindDegraded(ctx, reason)
	logging.BindSummary(ctx, obs.ResponseVisibleBytes(n), obs.ResponseRawBytes(n))
	return 1
}

func (d Deps) degradedSig(ctx context.Context, sig *capture.Signal) int {
	line := obs.DegradedPhrase(sig.Reason)
	if sig.Detail != "" {
		line += "\n" + sig.Detail
	}
	n, _ := fmt.Fprintln(d.Stderr, line)
	bindDegraded(ctx, sig.Reason)
	logging.BindSummary(ctx, obs.ResponseVisibleBytes(n), obs.ResponseRawBytes(n))
	return 1
}

func (d Deps) gcSample() bool {
	rate := d.Cfg.Calm.GCSampleRate
	if rate <= 0 {
		return false
	}
	//nolint:gosec // a reclamation sampling coin flip is not security-sensitive
	return rand.IntN(rate) == 0
}

func (d Deps) sessionTTL() time.Duration {
	return time.Duration(d.Cfg.Calm.SessionTTLMinutes) * time.Minute
}
