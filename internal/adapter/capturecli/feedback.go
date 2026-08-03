// Copyright 2026 The CALM Authors
// SPDX-License-Identifier: Apache-2.0

package capturecli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"net/http"
	"time"

	"github.com/google/uuid"
	logging "github.com/one-harsh/context-logging"

	"github.com/one-harsh/calm/internal/adapter/calm"
	"github.com/one-harsh/calm/internal/adapter/obs"
)

func (d Deps) feedbackCmd(ctx context.Context, args []string) int {
	fs := flag.NewFlagSet("feedback", flag.ContinueOnError)
	fs.SetOutput(d.Stderr)
	sessionID := fs.String("session", "", "harness conversation id")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	rest := fs.Args()
	if len(rest) != 2 {
		_, _ = fmt.Fprintln(d.Stderr, "usage: calm-capture feedback [--session <id>] <ref> <outcome>")
		return 2
	}
	ref, outcome := rest[0], rest[1]
	if !validOutcome(outcome) {
		_, _ = fmt.Fprintf(d.Stderr, "calm-capture feedback: outcome must be one of success|retry|degraded (got %q)\n", outcome)
		return 2
	}

	start := time.Now()
	ctx = withCallSummary(ctx)
	// Invalid refs cannot be truthful correlation join keys.
	if u, err := uuid.Parse(ref); err == nil {
		logging.BindSummary(ctx, obs.CorrelationID(u.String()))
	}
	defer func() {
		d.Logger.SummaryWithContext(ctx).Info(
			"feedback completed",
			obs.CallDurationMs(time.Since(start).Milliseconds()),
		)
	}()

	mgr, err := d.manager(sessionIDOr(*sessionID))
	if err != nil {
		return d.degradedStderr(ctx, obs.DegradedReasonCaptureFailed)
	}
	view, err := mgr.View(ctx)
	if err != nil {
		return d.degradedStderr(ctx, obs.DegradedReasonCaptureFailed)
	}
	if view.AuthFailed {
		return d.degradedStderr(ctx, obs.DegradedReasonAuthFailed)
	}
	if view.Token == "" {
		return d.degradedStderr(ctx, obs.DegradedReasonCalmUnreachable)
	}

	fctx, cancel := context.WithTimeout(ctx, opTimeout)
	defer cancel()
	ferr := d.Client.Feedback(fctx, view.Token, ref, outcome)
	if ferr == nil {
		return 0
	}
	var se *calm.StatusError
	if errors.As(ferr, &se) {
		switch se.Code {
		case http.StatusConflict:
			n, _ := fmt.Fprintln(d.Stderr, "feedback already recorded for this reference.")
			logging.BindSummary(ctx, obs.ResponseVisibleBytes(n), obs.ResponseRawBytes(n))
			return 1
		case http.StatusGone:
			return d.degradedStderr(ctx, obs.DegradedReasonFeedbackWindowExpired)
		case http.StatusUnauthorized, http.StatusForbidden:
			// AD03: auth rejects latch; an unknown feedback ref does not replace a session.
			if sig := mgr.OnCallError(ctx, view.Token, ferr); sig != nil {
				return d.degradedSig(ctx, sig)
			}
			return d.degradedStderr(ctx, obs.DegradedReasonAuthFailed)
		case http.StatusNotFound:
			return d.degradedStderr(ctx, obs.DegradedReasonSessionLost)
		case http.StatusBadRequest:
			n, _ := fmt.Fprintln(d.Stderr, "feedback rejected: malformed reference.")
			logging.BindSummary(ctx, obs.ResponseVisibleBytes(n), obs.ResponseRawBytes(n))
			return 2
		}
	}
	return d.degradedStderr(ctx, obs.DegradedReasonCalmUnreachable)
}

func validOutcome(o string) bool {
	switch o {
	case "success", "retry", "degraded":
		return true
	default:
		return false
	}
}
