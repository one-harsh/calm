// Copyright 2026 The CALM Authors
// SPDX-License-Identifier: Apache-2.0

package capture

import (
	"context"

	logging "github.com/one-harsh/context-logging"

	"github.com/one-harsh/calm/internal/adapter/calm"
	"github.com/one-harsh/calm/internal/adapter/exec"
	"github.com/one-harsh/calm/internal/adapter/extract"
	"github.com/one-harsh/calm/internal/adapter/obs"
)

// Session is the engine's seam onto a shell's per-session state (DESIGN.md §4).
// The shell owns the credential, the monotonic capture sequence, and the token
// registry; the engine reaches them only through this interface so it depends
// on no shell.
type Session interface {
	// Ensure lazily establishes the CALM session (capture is the only
	// establishment trigger) and allocates this capture's sequence, or returns
	// a Signal when the session is unavailable — a degraded call never burns a
	// sequence number. Establishment and allocation share the one fallible
	// step so a shell may hold locks or touch disk inside it.
	Ensure(ctx context.Context) (EnsureResult, *Signal)
	// OnCallError classifies a session-level CALM-call failure (auth rejection,
	// session loss with its recovery) into a Signal, or nil when the error is
	// not session-level.
	OnCallError(ctx context.Context, failedToken string, err error) *Signal
	// Record hands the shell the capture's persisted delta: each source label
	// paired with the staleness token that validates its later retrieval.
	// Called at most once per capture, with the full delta, so a shell that
	// persists synchronously pays one storage round-trip. Storage and its
	// locking are shell-owned; the engine only reports the delta.
	Record(ctx context.Context, delta []SourceToken)
}

// EnsureResult is the per-capture slice of session state Ensure hands the
// engine: the session token and this capture's allocated sequence.
type EnsureResult struct {
	Token string
	Seq   int64
}

// SourceToken pairs a persisted source label with the staleness token that
// validates it on the shell's retrieval path.
type SourceToken struct {
	Source string
	Token  string
}

// Signal is the engine's degraded-state classification for one call: the closed
// obs.DegradedReason* value plus optional operator-facing detail (e.g. the CALM
// error text carried on calm_unreachable). Shells own how it renders.
type Signal struct {
	Reason string // obs.DegradedReason* value
	Detail string // optional; surfaced by the shell after the canonical phrasing
}

// Outcome is the engine's per-call result the shell maps onto its transport:
// the visible text, whether capture landed and under which source (for the
// operator surface), and the degraded classification (Reason empty when the
// call was not degraded; Detail accompanies Reason).
type Outcome struct {
	Visible  string
	Captured bool
	Source   string
	Reason   string // obs.DegradedReason* value; "" when not degraded
	Detail   string // optional detail accompanying Reason
}

// Spec carries one executed local action into the shared CALM capture pipeline.
// Ingest and Visible differ only for a ranged calm_read_file slice
// (capture-full-present-range per DESIGN.md §3); every other tool passes the
// same payload for both.
type Spec struct {
	Ingest  string
	Visible string
	Res     exec.Result
	// RangedView marks a deliberately-scoped calm_read_file slice so presentation
	// shows the slice verbatim (capped) rather than collapsing a range the agent
	// already narrowed into whole-file summary chrome.
	RangedView bool
	// Plan runs only after the session pre-checks pass — it receives the
	// engine-allocated capture sequence so history #<seq> numbering stays
	// exactly aligned with the calls that could proceed.
	Plan func(seq int64) (extract.Plan, error)
}

// Engine holds the CALM client, logger, and the shell's retrieval-affordance
// name (threaded into recall hints so a capture points back at the shell's own
// retrieval command).
type Engine struct {
	calm   calm.Client
	log    *logging.Logger
	recall string
}

// NewEngine builds the capture engine. recall is the shell's retrieval command
// name (the MCP shell passes calm_search) fused into recall hints.
func NewEngine(c calm.Client, log *logging.Logger, recall string) *Engine {
	return &Engine{calm: c, log: log, recall: recall}
}

// Capture is the tool-agnostic back half every capture tool shares: session
// pre-checks, staleness-token mint, preservation-first dual-write, token
// recording, presentation, and fire-and-forget events. The local action already
// ran — on any CALM failure the visible payload returns raw (never-worse),
// classified by the degradation Signal.
func (e *Engine) Capture(ctx context.Context, sess Session, spec Spec) (out Outcome) {
	out = Outcome{Visible: spec.Visible} // default; the recover keeps whatever out holds at panic time
	ctx = logging.BindSummary(ctx, obs.ResponseRawBytes(len(spec.Visible)))
	defer func() {
		if p := recover(); p != nil {
			logging.BindSummary(ctx, obs.PresentationModeFieldInline)
			e.log.WithContext(ctx).Warn("capture pipeline panicked; returning best-available output",
				obs.DegradedReasonFieldCaptureFailed, logging.AnyField("panic", p))
			out.Reason = obs.DegradedReasonCaptureFailed
		}
	}()

	// Capture is the only establishment trigger by design: a process that
	// never had a session has no captures for search to find, so search's
	// calm_unreachable error stays more honest than a fresh session's
	// "no matches".
	res, sig := sess.Ensure(ctx)
	if sig != nil {
		logging.BindSummary(ctx, obs.PresentationModeFieldInline)
		if sig.Reason == obs.DegradedReasonCalmUnreachable {
			e.log.WithContext(ctx).Warn("CALM unavailable; returning raw output",
				obs.DegradedReasonFieldCalmUnreachable)
		}
		return Outcome{Visible: spec.Visible, Reason: sig.Reason, Detail: sig.Detail}
	}

	plan, derr := spec.Plan(res.Seq)
	if derr != nil {
		logging.BindSummary(ctx, obs.PresentationModeFieldInline)
		e.log.WithContext(ctx).Warn("derive plan failed; returning raw output",
			obs.DegradedReasonFieldCaptureFailed, logging.ErrorField(derr))
		return Outcome{Visible: spec.Visible, Reason: obs.DegradedReasonCaptureFailed}
	}
	plan.Token = extract.MintToken()

	outcomes, rep, sessErr := e.dualWriteIngest(ctx, res.Token, plan, spec.Ingest)
	if sessErr != nil {
		logging.BindSummary(ctx, obs.PresentationModeFieldInline)
		var reason, detail string
		if s := sess.OnCallError(ctx, res.Token, sessErr); s != nil {
			reason, detail = s.Reason, s.Detail
		}
		return Outcome{Visible: spec.Visible, Reason: reason, Detail: detail}
	}
	e.recordPersistedTokens(ctx, sess, plan, outcomes)
	out = e.formatCaptureOutcome(ctx, outcomes, rep, spec.Visible, spec.Res, plan.Token, spec.RangedView)

	// Fire-and-forget: events are pure observability and must never delay the response
	// (never-worse) — a stalled /v1/events can't hold the tool call hostage.
	if ev := extract.FinalizeEvents(plan, outcomes); len(ev) > 0 {
		e.emitEvents(ctx, res.Token, ev)
	}

	return
}
