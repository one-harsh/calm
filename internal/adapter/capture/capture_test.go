// Copyright 2026 The CALM Authors
// SPDX-License-Identifier: Apache-2.0

package capture

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/mock"

	logging "github.com/one-harsh/context-logging"

	"github.com/one-harsh/calm/internal/adapter/calm"
	"github.com/one-harsh/calm/internal/adapter/exec"
	"github.com/one-harsh/calm/internal/adapter/extract"
	"github.com/one-harsh/calm/internal/adapter/obs"
)

type stubSession struct {
	reg       *Registry
	seq       atomic.Int64
	token     string
	ensureSig *Signal
	onCallSig *Signal
}

func (s *stubSession) Ensure(context.Context) (EnsureResult, *Signal) {
	if s.ensureSig != nil {
		return EnsureResult{}, s.ensureSig
	}
	return EnsureResult{Token: s.token, Seq: s.seq.Add(1)}, nil
}
func (s *stubSession) OnCallError(context.Context, string, error) *Signal { return s.onCallSig }

// Record captures the persisted-delta handoff so tests assert the engine recorded
// the delta under the capture's token.
func (s *stubSession) Record(_ context.Context, token string, delta []SourceToken) {
	if token != s.token {
		return
	}
	for _, st := range delta {
		s.reg.Record(st.Source, st.Token)
	}
}

// stubSink captures the finalized-event handoff so tests assert the engine
// enqueued events under the capture's token — delivery itself is shell-owned.
type stubSink struct {
	deliveredTok  string
	deliveredEvts [][]calm.EventInput
}

func (s *stubSink) Enqueue(_ context.Context, token string, events []calm.EventInput) {
	s.deliveredTok = token
	s.deliveredEvts = append(s.deliveredEvts, events)
}

func planFor(command, raw string) func(int64) (extract.Plan, error) {
	return func(seq int64) (extract.Plan, error) {
		return extract.DerivePlan(
			extract.Invocation{Seq: seq, Command: command},
			extract.ExecResult{Stdout: raw},
		)
	}
}

// The happy path drives the full pipeline: dual-write (history then latest),
// finalized-event delivery, token recording, and summary-mode presentation with
// the fused recall label. Both sources are recorded so their fused labels
// validate later.
func TestCapture_Happy_DualWriteDeliversAndRecords(t *testing.T) {
	m := calm.NewMockClient(t)
	raw := strings.Repeat("x", inlineMaxBytes+1) // force summary mode so the recall label appears
	m.EXPECT().Ingest(mock.Anything, "tok-1", mock.MatchedBy(func(in calm.IngestInput) bool {
		return in.Source == "calm:v1:vcs:git:status#1"
	})).Return(calm.IngestSummary{Source: "calm:v1:vcs:git:status#1", SectionsIndexed: 1, SectionsTotal: 1}, nil).Once()
	m.EXPECT().Ingest(mock.Anything, "tok-1", mock.MatchedBy(func(in calm.IngestInput) bool {
		return in.Source == "calm:v1:vcs:git:status"
	})).Return(calm.IngestSummary{Source: "calm:v1:vcs:git:status", SectionsIndexed: 1, SectionsTotal: 1, CorrelationID: "corr-happy"}, nil).Once()

	sess := &stubSession{reg: NewRegistry(), token: "tok-1"}
	sink := &stubSink{}
	e := NewEngine(m, sess, sink, logging.Nop(), "calm_search")

	out := e.Capture(context.Background(), Spec{Ingest: raw, Visible: raw, Res: exec.Result{}, Plan: planFor("git status", raw)})

	if out.Reason != "" {
		t.Fatalf("reason = %q; want no degradation", out.Reason)
	}
	if !out.Captured || out.Source != "calm:v1:vcs:git:status" {
		t.Errorf("captured=%v source=%q; want captured under the latest source", out.Captured, out.Source)
	}
	if !strings.Contains(out.Visible, "calm_search source=calm:v1:vcs:git:status@") {
		t.Errorf("visible must carry the fused recall label; got:\n%s", out.Visible)
	}
	if out.FeedbackRef != "corr-happy" {
		t.Errorf("feedback ref = %q; want the primary source's ingest correlation id", out.FeedbackRef)
	}
	if !strings.HasPrefix(out.Label, "calm:v1:vcs:git:status@") {
		t.Errorf("outcome label = %q; want the fused primary source label", out.Label)
	}
	snap := sess.reg.Snapshot()
	if snap["calm:v1:vcs:git:status"] == "" || snap["calm:v1:vcs:git:status#1"] == "" {
		t.Errorf("both persisted sources must be recorded; snapshot = %v", snap)
	}
	// Delivery is shell-owned: the engine finalizes events and hands one batch
	// to the sink under the capture's token — it never writes them itself.
	if len(sink.deliveredEvts) != 1 || sink.deliveredTok != "tok-1" {
		t.Errorf("delivered = %d batch(es) under token %q; want 1 under tok-1", len(sink.deliveredEvts), sink.deliveredTok)
	}
}

// The load-bearing Outcome mapping: an Ensure degradation returns the raw
// payload with its reason AND its operator-facing detail intact (calm_unreachable
// carries the CALM error text), and no CALM traffic is attempted.
func TestCapture_EnsureUnreachable_PropagatesReasonAndDetail(t *testing.T) {
	m := calm.NewMockClient(t) // strict: any CALM call fails the test
	sess := &stubSession{
		reg:       NewRegistry(),
		ensureSig: &Signal{Reason: obs.DegradedReasonCalmUnreachable, Detail: "dial tcp: connection refused"},
	}
	e := NewEngine(m, sess, &stubSink{}, logging.Nop(), "calm_search")

	out := e.Capture(context.Background(), Spec{Ingest: "hi\n", Visible: "hi\n", Plan: planFor("echo hi", "hi\n")})

	if out.Visible != "hi\n" {
		t.Errorf("visible = %q; want raw preserved (never-worse)", out.Visible)
	}
	if out.Reason != obs.DegradedReasonCalmUnreachable {
		t.Errorf("reason = %q; want calm_unreachable", out.Reason)
	}
	if out.Detail != "dial tcp: connection refused" {
		t.Errorf("detail = %q; want the CALM error text propagated", out.Detail)
	}
}

// A session-level ingest error routes through OnCallError, whose classification
// (session_lost here) becomes the Outcome reason over the raw payload.
func TestCapture_SessionLevelError_UsesOnCallError(t *testing.T) {
	m := calm.NewMockClient(t)
	m.EXPECT().Ingest(mock.Anything, "tok-1", mock.Anything).
		Return(calm.IngestSummary{}, calm.ErrSessionNotFound).Once()
	sess := &stubSession{reg: NewRegistry(), token: "tok-1", onCallSig: &Signal{Reason: obs.DegradedReasonSessionLost}}
	e := NewEngine(m, sess, &stubSink{}, logging.Nop(), "calm_search")

	out := e.Capture(context.Background(), Spec{Ingest: "hi\n", Visible: "hi\n", Plan: planFor("echo hi", "hi\n")})

	if out.Visible != "hi\n" || out.Reason != obs.DegradedReasonSessionLost {
		t.Errorf("outcome = %+v; want raw payload with session_lost", out)
	}
}

// A non-session ingest failure is capture_failed: the raw payload returns, the
// best-effort tool event still fires, and nothing is recorded as persisted.
func TestCapture_IngestFailure_CaptureFailed(t *testing.T) {
	m := calm.NewMockClient(t)
	m.EXPECT().Ingest(mock.Anything, "tok-1", mock.Anything).
		Return(calm.IngestSummary{}, errors.New("boom")).Once()
	sess := &stubSession{reg: NewRegistry(), token: "tok-1"}
	sink := &stubSink{}
	e := NewEngine(m, sess, sink, logging.Nop(), "calm_search")

	out := e.Capture(context.Background(), Spec{Ingest: "hi\n", Visible: "hi\n", Plan: planFor("echo hi", "hi\n")})

	if out.Visible != "hi\n" || out.Reason != obs.DegradedReasonCaptureFailed {
		t.Errorf("outcome = %+v; want raw payload with capture_failed", out)
	}
	if out.Captured {
		t.Errorf("captured must be false when nothing persisted")
	}
	// The best-effort tool event still hands off to the delivery sink.
	if len(sink.deliveredEvts) != 1 {
		t.Errorf("delivered = %d batch(es); want the best-effort event handed to the sink", len(sink.deliveredEvts))
	}
}

// A plan-derivation failure (untranslatable command) degrades to capture_failed
// over the raw payload without any CALM traffic.
func TestCapture_PlanError_CaptureFailed(t *testing.T) {
	m := calm.NewMockClient(t) // strict: no ingest/events on a plan failure
	sess := &stubSession{reg: NewRegistry(), token: "tok-1"}
	e := NewEngine(m, sess, &stubSink{}, logging.Nop(), "calm_search")

	out := e.Capture(context.Background(), Spec{
		Ingest:  "hi\n",
		Visible: "hi\n",
		Plan:    func(int64) (extract.Plan, error) { return extract.Plan{}, errors.New("untranslatable") },
	})

	if out.Visible != "hi\n" || out.Reason != obs.DegradedReasonCaptureFailed {
		t.Errorf("outcome = %+v; want raw payload with capture_failed", out)
	}
}

// The strategy skips the sink when a unit finalizes to zero events, rather than
// handing it an empty batch — DeriveEvents always yields the tool-invocation
// draft, so this drives the strategy with an empty draft set directly.
func TestCapture_NoEvents_SinkNotCalled(t *testing.T) {
	m := calm.NewMockClient(t)
	m.EXPECT().Ingest(mock.Anything, "tok-1", mock.Anything).
		Return(calm.IngestSummary{Source: "s", SectionsIndexed: 1, SectionsTotal: 1}, nil).Once()
	sink := &stubSink{}
	f := fanOut{calm: m, log: logging.Nop(), events: sink}

	d := f.Deliver(context.Background(), "tok-1", CaptureUnit{Plan: extract.Plan{LatestSource: "s"}, Content: "hi\n"})

	if d.Err != nil {
		t.Fatalf("deliver err = %v; want success", d.Err)
	}
	if len(d.Events) != 0 {
		t.Errorf("finalized events = %d; want 0 for an empty draft set", len(d.Events))
	}
	if len(sink.deliveredEvts) != 0 {
		t.Errorf("sink called %d time(s); want 0 for an empty batch", len(sink.deliveredEvts))
	}
}
