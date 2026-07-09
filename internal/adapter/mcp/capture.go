// Copyright 2026 The CALM Authors
// SPDX-License-Identifier: Apache-2.0

package mcp

import (
	"context"

	logging "github.com/one-harsh/context-logging"

	"github.com/one-harsh/calm/internal/adapter/exec"
	"github.com/one-harsh/calm/internal/adapter/extract"
	"github.com/one-harsh/calm/internal/adapter/obs"
)

// captureSpec carries one executed local action into the shared CALM capture
// pipeline. ingest and visible differ only for ranged calm_read_file
// (capture-full-present-range per DESIGN.md §4); every other tool passes the
// same payload for both.
type captureSpec struct {
	ingest  string
	visible string
	res     exec.Result
	// plan runs only after the session pre-checks pass — the session-local
	// seq must be allocated exactly when a capture can proceed, or history
	// #<seq> numbering would drift on degraded calls.
	plan func() (extract.Plan, error)
}

// capturePipeline is the tool-agnostic back half every capture tool shares:
// session pre-checks, staleness-token mint, preservation-first dual-write,
// token recording, presentation, and fire-and-forget events. The local action
// already ran — on any CALM failure the visible payload returns raw
// (never-worse), phrased by the degradation signal.
func (s *Server) capturePipeline(ctx context.Context, spec captureSpec) (res ToolResult, err error) {
	res = TextResult(spec.visible, false) // default; the recover keeps whatever res holds at panic time
	ctx = logging.BindSummary(ctx, obs.ResponseRawBytes(len(spec.visible)))
	defer func() {
		if p := recover(); p != nil {
			logging.BindSummary(ctx, obs.PresentationModeFieldInline)
			s.log.WithContext(ctx).Warn("capture pipeline panicked; returning best-available output",
				obs.DegradedReasonFieldCaptureFailed, logging.AnyField("panic", p))
			err = &DegradedSignal{Reason: obs.DegradedReasonCaptureFailed}
		}
	}()

	// Capture is the only establishment trigger by design: a process that
	// never had a session has no captures for search to find, so search's
	// calm_unreachable error stays more honest than a fresh session's
	// "no matches".
	token, sig := s.ensureSession(ctx)
	if sig != nil {
		logging.BindSummary(ctx, obs.PresentationModeFieldInline)
		if sig.Reason == obs.DegradedReasonCalmUnreachable {
			s.log.WithContext(ctx).Warn("CALM unavailable; returning raw output",
				obs.DegradedReasonFieldCalmUnreachable)
		}
		return TextResult(spec.visible, false), sig
	}

	plan, derr := spec.plan()
	if derr != nil {
		logging.BindSummary(ctx, obs.PresentationModeFieldInline)
		s.log.WithContext(ctx).Warn("derive plan failed; returning raw output",
			obs.DegradedReasonFieldCaptureFailed, logging.ErrorField(derr))
		return TextResult(spec.visible, false), &DegradedSignal{Reason: obs.DegradedReasonCaptureFailed}
	}
	plan.Token = extract.MintToken()

	outcomes, rep, sessErr := s.dualWriteIngest(ctx, token, plan, spec.ingest)
	if sessErr != nil {
		logging.BindSummary(ctx, obs.PresentationModeFieldInline)
		return TextResult(spec.visible, false), s.sessionFailureSignal(ctx, token, sessErr)
	}
	s.recordPersistedTokens(plan, outcomes)
	res, err = s.formatCaptureOutcome(ctx, outcomes, rep, spec.visible, spec.res, plan.Token)

	// Fire-and-forget: events are pure observability and must never delay the response
	// (never-worse) — a stalled /v1/events can't hold the tool call hostage.
	if ev := extract.FinalizeEvents(plan, outcomes); len(ev) > 0 {
		s.emitEvents(ctx, token, ev)
	}

	return
}
