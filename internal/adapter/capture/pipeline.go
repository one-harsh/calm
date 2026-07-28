// Copyright 2026 The CALM Authors
// SPDX-License-Identifier: Apache-2.0

package capture

import (
	"context"
	"errors"
	"time"

	logging "github.com/one-harsh/context-logging"

	"github.com/one-harsh/calm/internal/adapter/calm"
	"github.com/one-harsh/calm/internal/adapter/extract"
)

const ingestTimeout = 10 * time.Second

type fanOut struct {
	calm   calm.Client
	log    *logging.Logger
	events EventSink
}

func (f fanOut) Deliver(ctx context.Context, token string, unit CaptureUnit) Delivery {
	outcomes, rep, err := f.dualWriteIngest(ctx, token, unit.Plan, unit.Content)
	if err != nil {
		return Delivery{Unit: unit, Outcomes: outcomes, Err: err}
	}
	events := extract.ApplyOutcomes(unit.Events, outcomes)
	if len(events) > 0 {
		f.events.Enqueue(ctx, token, events)
	}
	return Delivery{
		Unit:     unit,
		Outcomes: outcomes,
		Summary:  rep,
		Events:   events,
		Delta:    persistedDelta(unit.Plan, outcomes),
	}
}

// dualWriteIngest runs the preservation-first dual-write per LABELING.md
// (history first, then latest), recording per-source persisted outcomes and
// returning the preferred summary (latest wins when both succeed). The error
// is non-nil only for session-level failures; the first one short-circuits
// the remaining write — it would fail identically against the same dead token.
func (f fanOut) dualWriteIngest(ctx context.Context, token string, plan extract.Plan, raw string) ([]extract.WriteOutcome, *calm.IngestSummary, error) {
	var outcomes []extract.WriteOutcome
	var rep *calm.IngestSummary
	if plan.HistorySource != "" {
		sum, err := f.ingest(ctx, token, plan.HistorySource, raw, plan)
		outcomes = append(outcomes, extract.WriteOutcome{Source: plan.HistorySource, Persisted: err == nil})
		switch {
		case err == nil:
			rep = &sum
		case isSessionLevel(err):
			return outcomes, nil, err
		default:
			f.log.WithContext(ctx).Warn("history ingest failed",
				logging.StringField("source", plan.HistorySource), logging.ErrorField(err))
		}
	}
	if plan.LatestSource != "" {
		sum, err := f.ingest(ctx, token, plan.LatestSource, raw, plan)
		outcomes = append(outcomes, extract.WriteOutcome{Source: plan.LatestSource, Persisted: err == nil})
		switch {
		case err == nil:
			rep = &sum
		case isSessionLevel(err):
			return outcomes, nil, err
		default:
			f.log.WithContext(ctx).Warn("latest ingest failed",
				logging.StringField("source", plan.LatestSource), logging.ErrorField(err))
		}
	}
	return outcomes, rep, nil
}

func isSessionLevel(err error) bool {
	return errors.Is(err, calm.ErrSessionNotFound) || errors.Is(err, calm.ErrAuthRejected)
}

// persistedDelta pairs `plan.Token` with each source that actually persisted, so
// later `<shell retrieval> source=<fused>` calls validate. A source that failed
// to persist isn't included — its fused label would resolve to nothing on the
// CALM side anyway, so admitting the token would only surface a misleading empty
// result instead of the honest session_lost signal (or plain failure).
func persistedDelta(plan extract.Plan, outcomes []extract.WriteOutcome) []SourceToken {
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
	return delta
}

func (f fanOut) ingest(ctx context.Context, token, source, content string, plan extract.Plan) (calm.IngestSummary, error) {
	ictx, cancel := context.WithTimeout(ctx, ingestTimeout)
	defer cancel()
	return f.calm.Ingest(ictx, token, calm.IngestInput{
		Source:      source,
		Content:     content,
		ContentType: plan.ContentType,
		Format:      plan.Format,
	})
}
