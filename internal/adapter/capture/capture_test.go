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

// MOCKERY-ESCAPE: a generated double for capture.Session waits until the seam
// has a second shell implementation; this minimal in-package stub drives the
// engine's own pipeline tests.
type stubSession struct {
	reg       *Registry
	seq       atomic.Int64
	token     string
	ensureSig *Signal
	onCallSig *Signal
	emitToken string
	emitted   [][]calm.EventInput
}

func (s *stubSession) Ensure(context.Context) (EnsureResult, *Signal) {
	if s.ensureSig != nil {
		return EnsureResult{}, s.ensureSig
	}
	return EnsureResult{Token: s.token, Seq: s.seq.Add(1)}, nil
}
func (s *stubSession) OnCallError(context.Context, string, error) *Signal { return s.onCallSig }

func (s *stubSession) Record(_ context.Context, token string, delta []SourceToken) {
	if token != s.token {
		return
	}
	for _, st := range delta {
		s.reg.Record(st.Source, st.Token)
	}
}

// Emit records the delivery-seam handoff so tests assert the engine finalized
// and handed events to the shell — delivery itself is shell-owned.
func (s *stubSession) Emit(_ context.Context, token string, events []calm.EventInput) {
	s.emitToken = token
	s.emitted = append(s.emitted, events)
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
// token recording, summary-mode presentation with the fused recall label, and
// fire-and-forget event emission. Both sources are recorded so their fused
// labels validate later.
func TestCapture_Happy_DualWriteRecordsAndEmits(t *testing.T) {
	m := calm.NewMockClient(t)
	raw := strings.Repeat("x", inlineMaxBytes+1) // force summary mode so the recall label appears
	m.EXPECT().Ingest(mock.Anything, "tok-1", mock.MatchedBy(func(in calm.IngestInput) bool {
		return in.Source == "calm:v1:vcs:git:status#1"
	})).Return(calm.IngestSummary{Source: "calm:v1:vcs:git:status#1", SectionsIndexed: 1, SectionsTotal: 1}, nil).Once()
	m.EXPECT().Ingest(mock.Anything, "tok-1", mock.MatchedBy(func(in calm.IngestInput) bool {
		return in.Source == "calm:v1:vcs:git:status"
	})).Return(calm.IngestSummary{Source: "calm:v1:vcs:git:status", SectionsIndexed: 1, SectionsTotal: 1}, nil).Once()

	sess := &stubSession{reg: NewRegistry(), token: "tok-1"}
	e := NewEngine(m, logging.Nop(), "calm_search")

	out := e.Capture(context.Background(), sess, Spec{Ingest: raw, Visible: raw, Res: exec.Result{}, Plan: planFor("git status", raw)})

	if out.Reason != "" {
		t.Fatalf("reason = %q; want no degradation", out.Reason)
	}
	if !out.Captured || out.Source != "calm:v1:vcs:git:status" {
		t.Errorf("captured=%v source=%q; want captured under the latest source", out.Captured, out.Source)
	}
	if !strings.Contains(out.Visible, "calm_search source=calm:v1:vcs:git:status@") {
		t.Errorf("visible must carry the fused recall label; got:\n%s", out.Visible)
	}
	snap := sess.reg.Snapshot()
	if snap["calm:v1:vcs:git:status"] == "" || snap["calm:v1:vcs:git:status#1"] == "" {
		t.Errorf("both persisted sources must be recorded; snapshot = %v", snap)
	}
	// Delivery is shell-owned: the engine finalizes events and hands one batch
	// to the seam under the capture's token — it never writes them itself.
	if len(sess.emitted) != 1 || sess.emitToken != "tok-1" {
		t.Errorf("emitted = %d batch(es) under token %q; want 1 under tok-1", len(sess.emitted), sess.emitToken)
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
	e := NewEngine(m, logging.Nop(), "calm_search")

	out := e.Capture(context.Background(), sess, Spec{Ingest: "hi\n", Visible: "hi\n", Plan: planFor("echo hi", "hi\n")})

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
	e := NewEngine(m, logging.Nop(), "calm_search")

	out := e.Capture(context.Background(), sess, Spec{Ingest: "hi\n", Visible: "hi\n", Plan: planFor("echo hi", "hi\n")})

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
	e := NewEngine(m, logging.Nop(), "calm_search")

	out := e.Capture(context.Background(), sess, Spec{Ingest: "hi\n", Visible: "hi\n", Plan: planFor("echo hi", "hi\n")})

	if out.Visible != "hi\n" || out.Reason != obs.DegradedReasonCaptureFailed {
		t.Errorf("outcome = %+v; want raw payload with capture_failed", out)
	}
	if out.Captured {
		t.Errorf("captured must be false when nothing persisted")
	}
	// The best-effort tool event still hands off to the delivery seam.
	if len(sess.emitted) != 1 {
		t.Errorf("emitted = %d batch(es); want the best-effort event handed to the seam", len(sess.emitted))
	}
}

// A plan-derivation failure (untranslatable command) degrades to capture_failed
// over the raw payload without any CALM traffic.
func TestCapture_PlanError_CaptureFailed(t *testing.T) {
	m := calm.NewMockClient(t) // strict: no ingest/events on a plan failure
	sess := &stubSession{reg: NewRegistry(), token: "tok-1"}
	e := NewEngine(m, logging.Nop(), "calm_search")

	out := e.Capture(context.Background(), sess, Spec{
		Ingest:  "hi\n",
		Visible: "hi\n",
		Plan:    func(int64) (extract.Plan, error) { return extract.Plan{}, errors.New("untranslatable") },
	})

	if out.Visible != "hi\n" || out.Reason != obs.DegradedReasonCaptureFailed {
		t.Errorf("outcome = %+v; want raw payload with capture_failed", out)
	}
}
