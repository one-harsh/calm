// Copyright 2026 The CALM Authors
// SPDX-License-Identifier: Apache-2.0

package capturecli

import (
	"context"
	"fmt"
	"strings"

	logging "github.com/one-harsh/context-logging"

	"github.com/one-harsh/calm/internal/adapter/capture"
	"github.com/one-harsh/calm/internal/adapter/capturecli/harness"
	"github.com/one-harsh/calm/internal/adapter/exec"
	"github.com/one-harsh/calm/internal/adapter/extract"
	"github.com/one-harsh/calm/internal/adapter/session"
)

// handleObserve captures an already-executed command and, when the harness honors
// it, replaces the raw result with the engine's presentation. It mirrors execCmd
// minus the run.
func (c hookConfig) handleObserve(ctx context.Context, ev harness.ObserveEvent) int {
	if ev.SessionID == "" || strings.TrimSpace(ev.Command) == "" || ev.IsImage {
		return hookPassThrough
	}
	// AD07: a command that invokes calm-capture in any pipeline segment passes
	// through — plumbing must not re-ingest capture's own output.
	if extract.InvokesProgram(ev.Command, binaryName) {
		return hookPassThrough
	}
	if c.root == "" || c.deps.Client == nil {
		return hookPassThrough
	}

	mgr, err := c.deps.manager(ev.SessionID)
	if err != nil {
		return hookPassThrough
	}
	c.deps.drain(ctx, mgr)

	r := exec.Result{Stdout: ev.Stdout, Stderr: ev.Stderr, ExitCode: ev.ExitCode, Truncated: ev.Truncated}
	engine := capture.NewEngine(c.deps.Client, mgr, mgr, c.deps.Logger, recallFor(c.binPath, ev.SessionID), capture.WithDiscoveryCard())
	raw := capture.CommandPayload(r)
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

	// never-worse: a degraded, uncaptured, or replacement-ignored (CanReplace)
	// outcome leaves the native result untouched — capture-only, no replacement.
	if out.Reason != "" || !out.Captured || !ev.CanReplace {
		return 0
	}
	wire := harness.Claude.RenderObserve(harness.ObserveResponse{Stdout: composeExec(out), Interrupted: ev.Interrupted})
	if wire == nil {
		return hookPassThrough
	}
	_, _ = fmt.Fprintln(c.stdout, string(wire))
	return 0
}
