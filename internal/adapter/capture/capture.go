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
	// token names the session generation that captured — a shell discards a
	// delta whose session has since been replaced, so dead-generation labels
	// surface as staleness later, never as valid refs into the new session
	// (honest capture continuity). Called at most once per capture, with the
	// full delta. Storage and its locking are shell-owned.
	Record(ctx context.Context, token string, delta []SourceToken)
	// Emit enqueues this capture's finalized events for delivery by the shell's
	// strategy. It must return without blocking on network — the engine calls
	// it in-call, so delivery never delays the response (never-worse); queue
	// durability is shell-owned (the MCP shell's queue is its process lifetime,
	// the capture shell's is the on-disk spool). token names the generation
	// that captured: a shell rejects a replaced generation's events before
	// enqueue. Called at most once per capture, with a non-empty batch.
	Emit(ctx context.Context, token string, events []calm.EventInput)
}

type EnsureResult struct {
	Token string // Session token
	Seq   int64
}

type SourceToken struct {
	Source string
	Token  string
}

type Signal struct {
	Reason string // obs.DegradedReason* value
	Detail string // optional; surfaced by the shell after the canonical phrasing
}

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
	Ingest     string
	Visible    string
	Res        exec.Result
	RangedView bool
	Plan       func(seq int64) (extract.Plan, error)
}

type Engine struct {
	calm          calm.Client
	log           *logging.Logger
	recall        string
	discoveryCard bool
}

type Option func(*Engine)

func WithDiscoveryCard() Option {
	return func(e *Engine) { e.discoveryCard = true }
}

func NewEngine(c calm.Client, log *logging.Logger, recall string, opts ...Option) *Engine {
	e := &Engine{calm: c, log: log, recall: recall}
	for _, o := range opts {
		o(e)
	}
	return e
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
	e.recordPersistedTokens(ctx, sess, res.Token, plan, outcomes)
	out = e.formatCaptureOutcome(ctx, outcomes, rep, spec.Visible, spec.Res, plan.Token, spec.RangedView)

	// The retrieval-discovery card rides the session's first captured
	// presentation once; the persisted sequence makes "first" knowable.
	// Off for the MCP shell, so its output is unchanged.
	if e.discoveryCard && res.Seq == 1 && out.Captured {
		out.Visible = withDiscoveryCard(out.Visible, e.recall)
	}

	// Events are pure observability: either fire-and-forget or spool.
	if ev := extract.FinalizeEvents(plan, outcomes); len(ev) > 0 {
		sess.Emit(ctx, res.Token, ev)
	}

	return
}
