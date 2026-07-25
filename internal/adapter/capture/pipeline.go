// Copyright 2026 The CALM Authors
// SPDX-License-Identifier: Apache-2.0

package capture

import (
	"context"
	"errors"
	"time"

	logging "github.com/one-harsh/context-logging"

	"github.com/one-harsh/calm/internal/adapter/calm"
	"github.com/one-harsh/calm/internal/adapter/exec"
	"github.com/one-harsh/calm/internal/adapter/extract"
	"github.com/one-harsh/calm/internal/adapter/obs"
)

const (
	ingestTimeout = 10 * time.Second
	eventTimeout  = 5 * time.Second
)

// dualWriteIngest runs the preservation-first dual-write per LABELING.md
// (history first, then latest), recording per-source persisted outcomes and
// returning the preferred summary (latest wins when both succeed). The error
// is non-nil only for session-level failures; the first one short-circuits
// the remaining write — it would fail identically against the same dead token.
func (e *Engine) dualWriteIngest(ctx context.Context, token string, plan extract.Plan, raw string) ([]extract.WriteOutcome, *calm.IngestSummary, error) {
	var outcomes []extract.WriteOutcome
	var rep *calm.IngestSummary
	if plan.HistorySource != "" {
		sum, err := e.ingest(ctx, token, plan.HistorySource, raw, plan)
		outcomes = append(outcomes, extract.WriteOutcome{Source: plan.HistorySource, Persisted: err == nil})
		switch {
		case err == nil:
			rep = &sum
		case isSessionLevel(err):
			return outcomes, nil, err
		default:
			e.log.WithContext(ctx).Warn("history ingest failed",
				logging.StringField("source", plan.HistorySource), logging.ErrorField(err))
		}
	}
	if plan.LatestSource != "" {
		sum, err := e.ingest(ctx, token, plan.LatestSource, raw, plan)
		outcomes = append(outcomes, extract.WriteOutcome{Source: plan.LatestSource, Persisted: err == nil})
		switch {
		case err == nil:
			rep = &sum
		case isSessionLevel(err):
			return outcomes, nil, err
		default:
			e.log.WithContext(ctx).Warn("latest ingest failed",
				logging.StringField("source", plan.LatestSource), logging.ErrorField(err))
		}
	}
	return outcomes, rep, nil
}

func isSessionLevel(err error) bool {
	return errors.Is(err, calm.ErrSessionNotFound) || errors.Is(err, calm.ErrAuthRejected)
}

// formatCaptureOutcome classifies the dual-write outcome into one of three
// states (capture_failed / capture_partial / happy). It binds captured/source
// fields onto the summary for the partial+happy paths, returns the visible-text
// content, and classifies degradation via Outcome.Reason for the partial+failed
// paths — the shell layers the canonical phrasing prefix + degraded summary
// fields on top. `token` is the per-call staleness suffix fused into the
// recall-hint label.
func (e *Engine) formatCaptureOutcome(ctx context.Context, outcomes []extract.WriteOutcome, rep *calm.IngestSummary, raw string, r exec.Result, token string, rangedView bool) Outcome {
	anyFailed := false
	for _, o := range outcomes {
		if !o.Persisted {
			anyFailed = true
			break
		}
	}
	switch {
	case rep == nil:
		logging.BindSummary(ctx, obs.PresentationModeFieldInline)
		e.log.WithContext(ctx).Warn("all ingests failed; returning raw output",
			obs.DegradedReasonFieldCaptureFailed)
		return Outcome{Visible: raw, Reason: obs.DegradedReasonCaptureFailed}
	case anyFailed:
		logging.BindSummary(
			ctx,
			logging.BoolField(obs.KeyCaptured, true),
			obs.SourceLabel(rep.Source),
		)
		return Outcome{
			Visible:  presentCapture(ctx, *rep, raw, r, token, rangedView, e.recall),
			Captured: true,
			Source:   rep.Source,
			Reason:   obs.DegradedReasonCapturePartial,
		}
	default:
		logging.BindSummary(
			ctx,
			logging.BoolField(obs.KeyCaptured, true),
			obs.SourceLabel(rep.Source),
		)
		return Outcome{
			Visible:  presentCapture(ctx, *rep, raw, r, token, rangedView, e.recall),
			Captured: true,
			Source:   rep.Source,
		}
	}
}

// recordPersistedTokens registers `plan.Token` against each source that
// actually persisted, so later `<shell retrieval> source=<fused>` calls
// validate. A source that failed to persist isn't recorded — its fused label
// would resolve to nothing on the CALM side anyway, so admitting the token would
// only surface a misleading empty result instead of the honest session_lost
// signal (or plain failure).
func (e *Engine) recordPersistedTokens(ctx context.Context, sess Session, plan extract.Plan, outcomes []extract.WriteOutcome) {
	var delta []SourceToken
	for _, o := range outcomes {
		if !o.Persisted {
			continue
		}
		switch o.Source {
		case plan.LatestSource, plan.HistorySource:
			delta = append(delta, SourceToken{Source: o.Source, Token: plan.Token})
		}
	}
	if len(delta) > 0 {
		sess.Record(ctx, delta)
	}
}

func (e *Engine) emitEvents(ctx context.Context, token string, ev []calm.EventInput) {
	ectx := context.WithoutCancel(ctx)
	go func() {
		defer func() {
			if p := recover(); p != nil {
				e.log.WithContext(ectx).Warn("event emission panicked", logging.AnyField("panic", p))
			}
		}()
		wctx, cancel := context.WithTimeout(ectx, eventTimeout)
		defer cancel()
		// AD03: no recovery trigger here — every event write follows an ingest
		// on the same token, so either that ingest already recovered or the
		// next tool call will; recovering from this goroutine would add
		// concurrency surface for no visible benefit.
		if err := e.calm.WriteEvents(wctx, token, ev); err != nil {
			e.log.WithContext(wctx).Warn("write events failed", logging.ErrorField(err))
		}
	}()
}

func (e *Engine) ingest(ctx context.Context, token, source, content string, plan extract.Plan) (calm.IngestSummary, error) {
	ictx, cancel := context.WithTimeout(ctx, ingestTimeout)
	defer cancel()
	return e.calm.Ingest(ictx, token, calm.IngestInput{
		Source:      source,
		Content:     content,
		ContentType: plan.ContentType,
		Format:      plan.Format,
	})
}
