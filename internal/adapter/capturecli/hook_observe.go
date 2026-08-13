// Copyright 2026 The CALM Authors
// SPDX-License-Identifier: Apache-2.0

package capturecli

import (
	"context"
	"fmt"
	"strings"
	"time"

	logging "github.com/one-harsh/context-logging"

	"github.com/one-harsh/calm/internal/adapter/capture"
	"github.com/one-harsh/calm/internal/adapter/capturecli/harness"
	"github.com/one-harsh/calm/internal/adapter/exec"
	"github.com/one-harsh/calm/internal/adapter/extract"
	"github.com/one-harsh/calm/internal/adapter/obs"
	"github.com/one-harsh/calm/internal/adapter/session"
)

func (c hookConfig) handleObserve(ctx context.Context, ev harness.ObserveEvent, start time.Time) int {
	if ev.SessionID == "" || strings.TrimSpace(ev.Command) == "" || ev.IsImage {
		return hookPassThrough
	}
	// AD07: capture plumbing must not ingest its own output.
	if extract.InvokesProgram(ev.Command, binaryName) {
		return hookPassThrough
	}
	if c.root == "" || c.deps.Client == nil {
		return hookPassThrough
	}

	ctx = withCallSummary(ctx)
	// The engine's selected presentation is not model-visible until rendering succeeds.
	logging.BindSummary(ctx, obs.PresentationModeFieldInline, obs.ReplacedFieldFalse)
	r := exec.Result{Stdout: ev.Stdout, Stderr: ev.Stderr, ExitCode: ev.ExitCode, Truncated: ev.Truncated}
	raw := capture.CommandPayload(r)
	logging.BindSummary(ctx, obs.ResponseRawBytes(len(raw)))
	visible := raw
	defer func() {
		c.deps.Logger.SummaryWithContext(ctx).Info(
			"observation completed",
			obs.ResponseVisibleBytes(len(visible)),
			obs.CallDurationMs(time.Since(start).Milliseconds()),
		)
	}()

	mgr, err := c.deps.manager(ev.SessionID)
	if err != nil {
		return hookPassThrough
	}
	c.deps.drain(ctx, mgr)

	engine := capture.NewEngine(c.deps.Client, mgr, mgr, c.deps.Logger, recallFor(c.binPath, ev.SessionID), capture.WithDiscoveryCard())
	out := engine.Capture(ctx, capture.Spec{
		Ingest:  raw,
		Visible: raw,
		Res:     r,
		Plan: func(seq int64) (extract.Plan, error) {
			return extract.DerivePlan(
				extract.Invocation{Seq: seq, Command: ev.Command, Cwd: ev.Cwd, WorkspaceRoot: ev.Cwd},
				extract.ExecResult{Stdout: r.Stdout, Stderr: r.Stderr, ExitCode: r.ExitCode},
			)
		},
	})

	c.deps.drain(ctx, mgr)
	if c.deps.gcSample() {
		if gerr := session.GC(c.deps.Root, c.deps.sessionTTL()); gerr != nil {
			c.deps.Logger.WithContext(ctx).Warn("reclamation sweep failed", logging.ErrorField(gerr))
		}
	}

	if out.Reason != "" {
		bindDegraded(ctx, out.Reason)
	}
	// never-worse: record what reached context, not an unrendered engine choice.
	if out.Reason != "" || !out.Captured || !ev.CanReplace {
		logging.BindSummary(ctx, obs.PresentationModeFieldInline)
		return 0
	}
	presented := composeExec(out)
	wire := harness.Claude.RenderObserve(harness.ObserveResponse{Stdout: presented, Interrupted: ev.Interrupted})
	if wire == nil {
		logging.BindSummary(ctx, obs.PresentationModeFieldInline)
		return hookPassThrough
	}
	_, _ = fmt.Fprintln(c.stdout, string(wire))
	visible = presented
	logging.BindSummary(ctx, obs.ReplacedFieldTrue)
	return 0
}
