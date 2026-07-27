// Copyright 2026 The CALM Authors
// SPDX-License-Identifier: Apache-2.0

package capturecli

import (
	"context"
	"errors"
	"flag"
	"fmt"

	"github.com/one-harsh/calm/internal/adapter/calm"
)

func (d Deps) initCmd(ctx context.Context, args []string) int {
	fs := flag.NewFlagSet("init", flag.ContinueOnError)
	fs.SetOutput(d.Stderr)
	sessionID := fs.String("session", "", "harness conversation id (required with --reset)")
	reset := fs.Bool("reset", false, "clear a persisted auth latch and advance the session generation")
	if err := fs.Parse(args); err != nil {
		return 2
	}

	if *reset {
		if *sessionID == "" {
			_, _ = fmt.Fprintln(d.Stderr, "calm-capture init --reset requires --session <id>")
			return 2
		}
		mgr, err := d.manager(*sessionID)
		if err != nil {
			_, _ = fmt.Fprintln(d.Stderr, "calm-capture init: "+err.Error())
			return 1
		}
		if rerr := mgr.Reset(ctx); rerr != nil {
			_, _ = fmt.Fprintln(d.Stderr, "calm-capture init: reset failed: "+rerr.Error())
			return 1
		}
		_, _ = fmt.Fprintf(d.Stderr, "reset: cleared auth latch and advanced session generation for %q\n", *sessionID)
	}

	pctx, cancel := context.WithTimeout(ctx, opTimeout)
	defer cancel()
	switch _, perr := d.Client.RegisterClient(pctx, d.Cfg.Calm.Client); {
	case perr == nil:
		_, _ = fmt.Fprintf(d.Stderr, "ok: CALM reachable at %s; credentials accepted for client %q\n", d.Cfg.Calm.URL, d.Cfg.Calm.Client)
		return 0
	case errors.Is(perr, calm.ErrAuthRejected):
		_, _ = fmt.Fprintf(d.Stderr, "credential failure: CALM at %s rejected the namespace credential — check the api_key pairing\n", d.Cfg.Calm.URL)
		return 1
	default:
		_, _ = fmt.Fprintf(d.Stderr, "connectivity failure: cannot reach CALM at %s: %s\n", d.Cfg.Calm.URL, perr.Error())
		return 1
	}
}
