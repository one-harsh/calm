// Copyright 2026 The CALM Authors
// SPDX-License-Identifier: Apache-2.0

package capturecli

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	logging "github.com/one-harsh/context-logging"

	"github.com/one-harsh/calm/internal/adapter/capture"
	"github.com/one-harsh/calm/internal/adapter/exec"
	"github.com/one-harsh/calm/internal/adapter/extract"
	"github.com/one-harsh/calm/internal/adapter/obs"
	"github.com/one-harsh/calm/internal/adapter/session"
)

const drainTimeout time.Duration = 2 * time.Second

func (d Deps) execCmd(ctx context.Context, args []string) int {
	fs := flag.NewFlagSet("exec", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	sessionID := fs.String("session", "", "harness conversation id")
	if err := fs.Parse(args); err != nil {
		_, _ = fmt.Fprintln(d.Stdout, "calm-capture exec:", err)
		return 2
	}
	argv := fs.Args()
	command := strings.Join(argv, " ")
	if strings.TrimSpace(command) == "" {
		_, _ = fmt.Fprintln(d.Stdout, "calm-capture exec: no command after --")
		return 2
	}
	if argvAssignmentGuard(argv, d.Stdout) {
		return 2
	}

	if os.Getenv(CaptureActiveEnv) == "1" {
		return passthroughRun(ctx, argv, d.Stdout, d.Stderr)
	}

	start := time.Now()
	ctx = withCallSummary(ctx)
	defer func() {
		d.Logger.SummaryWithContext(ctx).Info(
			"command completed",
			obs.CallDurationMs(time.Since(start).Milliseconds()),
		)
	}()

	sid := sessionIDOr(*sessionID)
	mgr, err := d.manager(sid)
	if err != nil {
		n, _ := fmt.Fprintln(d.Stdout, "calm-capture exec:", err)
		logging.BindSummary(ctx, obs.ResponseVisibleBytes(n))
		return 2
	}

	d.drain(ctx, mgr)

	cwd, _ := os.Getwd()
	// never-worse: preserve native timeout ownership; only CALM work is bounded.
	// AD07: the child sentinel prevents nested capture wrappers.
	r, runErr := runWrapped(ctx, argv, cwd, CaptureActiveEnv+"=1")
	if runErr != nil {
		n, _ := fmt.Fprintln(d.Stdout, "failed to run command:", runErr)
		logging.BindSummary(ctx, obs.ResponseVisibleBytes(n))
		return 1
	}

	engine := capture.NewEngine(d.Client, mgr, mgr, d.Logger, recallFor(hookBinPath(), sid), capture.WithDiscoveryCard())
	raw := capture.CommandPayload(r)
	out := engine.Capture(ctx, capture.Spec{
		Ingest:      raw,
		Visible:     raw,
		Res:         r,
		Consumption: capture.ConsumptionWhole,
		Plan: func(seq int64) (extract.Plan, error) {
			return extract.DerivePlan(
				extract.Invocation{Seq: seq, Command: command, Cwd: cwd, WorkspaceRoot: cwd},
				extract.ExecResult{Stdout: r.Stdout, Stderr: r.Stderr, ExitCode: r.ExitCode, TimedOut: r.TimedOut},
			)
		},
	})

	if out.Reason != "" {
		bindDegraded(ctx, out.Reason)
	}
	// Response-first: deferred delivery cannot delay the agent-visible result.
	visible := composeExec(out)
	n, _ := fmt.Fprint(d.Stdout, visible)
	logging.BindSummary(ctx, obs.ResponseVisibleBytes(n))

	d.drain(ctx, mgr)
	if d.gcSample() {
		if gerr := session.GC(d.Root, d.sessionTTL()); gerr != nil {
			d.Logger.WithContext(ctx).Warn("reclamation sweep failed", logging.ErrorField(gerr))
		}
	}

	// never-worse: capture state never alters the wrapped command's exit code.
	return r.ExitCode
}

// Even inline captures need an address because the capture shell has no tool description.
func composeExec(out capture.Outcome) string {
	if out.Reason != "" {
		v := out.Visible
		if v != "" && !strings.HasSuffix(v, "\n") {
			v += "\n"
		}
		return v + obs.DegradedPhrase(out.Reason) + "\n"
	}
	if !out.Captured || out.Label == "" {
		return out.Visible
	}
	v := out.Visible
	if v != "" && !strings.HasSuffix(v, "\n") {
		v += "\n"
	}
	v += "↳ source=" + out.Label
	if out.FeedbackRef != "" {
		v += " · feedback: calm-capture feedback " + out.FeedbackRef
	}
	return v + "\n"
}

func (d Deps) drain(ctx context.Context, mgr *session.Manager) {
	dctx, cancel := context.WithTimeout(ctx, drainTimeout)
	defer cancel()
	mgr.Drain(dctx)
}

// Multi-argument exec cannot apply shell assignment prefixes; direct callers
// must use the single-string form instead.
func argvAssignmentGuard(argv []string, out io.Writer) bool {
	if len(argv) > 1 && extract.IsAssignmentPrefix(argv[0]) {
		_, _ = fmt.Fprintln(out, "calm-capture exec: assignment prefix needs the single-string form: exec -- 'FOO=bar cmd …'")
		return true
	}
	return false
}

// PassthroughExec is the never-worse bootstrap floor: it runs the local action
// without exposing bootstrap diagnostics on captured streams.
func PassthroughExec(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("exec", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	_ = fs.String("session", "", "harness conversation id")
	if err := fs.Parse(args); err != nil {
		_, _ = fmt.Fprintln(stdout, "calm-capture exec:", err)
		return 2
	}
	argv := fs.Args()
	if strings.TrimSpace(strings.Join(argv, " ")) == "" {
		_, _ = fmt.Fprintln(stdout, "calm-capture exec: no command after --")
		return 2
	}
	// Bootstrap degradation must not change argv validation semantics.
	if argvAssignmentGuard(argv, stdout) {
		return 2
	}
	if os.Getenv(CaptureActiveEnv) == "1" {
		return passthroughRun(ctx, argv, stdout, stderr)
	}
	cwd, _ := os.Getwd()
	r, runErr := runWrapped(ctx, argv, cwd, CaptureActiveEnv+"=1")
	if runErr != nil {
		_, _ = fmt.Fprintln(stdout, "failed to run command:", runErr)
		return 1
	}
	_, _ = fmt.Fprint(stdout, composeExec(capture.Outcome{
		Visible: capture.CommandPayload(r),
		Reason:  obs.DegradedReasonCaptureFailed,
	}))
	return r.ExitCode
}

func passthroughRun(ctx context.Context, argv []string, stdout, stderr io.Writer) int {
	cwd, _ := os.Getwd()
	r, err := runWrapped(ctx, argv, cwd)
	if err != nil {
		_, _ = fmt.Fprintln(stdout, "failed to run command:", err)
		return 1
	}
	_, _ = fmt.Fprint(stdout, r.Stdout)
	_, _ = fmt.Fprint(stderr, r.Stderr)
	return r.ExitCode
}

// One argument is the hook's shell program; multiple arguments are already
// tokenized and must bypass the shell to preserve quoting.
func runWrapped(ctx context.Context, argv []string, cwd string, extraEnv ...string) (exec.Result, error) {
	if len(argv) == 1 {
		return exec.Run(ctx, argv[0], cwd, extraEnv...)
	}
	return exec.RunArgv(ctx, argv, cwd, extraEnv...)
}
