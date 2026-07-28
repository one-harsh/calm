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

// Session is the engine's write-port onto a shell's per-session state: the sole
// owner of every mutation to the five-item session state — Ensure drives the
// sequence and establishment, OnCallError drives the auth latch and epoch, and
// Record drives the token registry — so the engine can enumerate every state
// write from one seam and depends on no shell.
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
	// Record merges the persisted registry delta under the shell's per-session
	// store. A delta whose session was replaced mid-call is discarded, so
	// dead-generation labels surface as staleness rather than validate against
	// the new session (honest capture continuity).
	Record(ctx context.Context, token string, delta []SourceToken)
}

// EventSink is the shell-provided transport for finalized capture events,
// consumed by the delivery strategy (DESIGN.md §10). Enqueue must not block on
// the network (never-worse); the shell delivers off the response path.
type EventSink interface {
	Enqueue(ctx context.Context, token string, events []calm.EventInput)
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
	Visible     string
	Captured    bool
	Source      string
	Reason      string // obs.DegradedReason* value; "" when not degraded
	Detail      string // optional detail accompanying Reason
	Label       string
	FeedbackRef string
}

type CaptureUnit struct {
	Plan    extract.Plan
	Content string
	Events  []extract.EventDraft
}

type Delivery struct {
	Unit     CaptureUnit // provenance: the unit this delivery carried
	Outcomes []extract.WriteOutcome
	Summary  *calm.IngestSummary // preferred summary (latest wins) for presentation
	Events   []calm.EventInput   // finalized (ApplyOutcomes), already handed to the sink
	Delta    []SourceToken       // registry delta for the sources that persisted
	Err      error
}

type DeliveryStrategy interface {
	Deliver(ctx context.Context, token string, unit CaptureUnit) Delivery
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
	sess     Session
	strategy DeliveryStrategy
	log      *logging.Logger
	present  presentOptions
}

type Option func(*Engine)

func WithDiscoveryCard() Option {
	return func(e *Engine) { e.present.discoveryCard = true }
}

func NewEngine(client calm.Client, sess Session, sink EventSink, log *logging.Logger, recall string, opts ...Option) *Engine {
	e := &Engine{
		sess:     sess,
		strategy: fanOut{calm: client, log: log, events: sink},
		log:      log,
		present:  presentOptions{recall: recall},
	}
	for _, o := range opts {
		o(e)
	}
	return e
}

// Capture is the engine's whole per-call surface:
//  1. ensure the session (a degraded Ensure burns no sequence number)
//  2. derive the plan, mint the staleness token
//  3. compose the capture unit
//  4. deliver it through the strategy (events ride the delivery)
//  5. record the registry delta through the session write-port
//  6. present (label, feedback ref, first-capture card)
func (e *Engine) Capture(ctx context.Context, spec Spec) (out Outcome) {
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

	res, sig := e.sess.Ensure(ctx)
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

	unit := newCaptureUnit(plan, spec.Ingest)
	d := e.strategy.Deliver(ctx, res.Token, unit)
	if d.Err != nil {
		logging.BindSummary(ctx, obs.PresentationModeFieldInline)
		var reason, detail string
		if s := e.sess.OnCallError(ctx, res.Token, d.Err); s != nil {
			reason, detail = s.Reason, s.Detail
		}
		return Outcome{Visible: spec.Visible, Reason: reason, Detail: detail}
	}
	e.sess.Record(ctx, res.Token, d.Delta)
	return present(ctx, e.log, d, spec, res.Seq, e.present)
}

// newCaptureUnit assembles one wrapped action's full capture set: its dual-write
// plan, the content to ingest, and the derived event drafts.
func newCaptureUnit(plan extract.Plan, content string) CaptureUnit {
	return CaptureUnit{Plan: plan, Content: content, Events: extract.DeriveEvents(plan)}
}
