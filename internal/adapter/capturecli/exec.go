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

	sid := sessionIDOr(*sessionID)
	mgr, err := d.manager(sid)
	if err != nil {
		_, _ = fmt.Fprintln(d.Stdout, "calm-capture exec:", err)
		return 2
	}

	d.drain(ctx, mgr)

	cwd, _ := os.Getwd()
	// The wrapped command runs unbounded: its duration is native cost, and the
	// harness/model own command timeouts — the wrap must not impose one the
	// native path lacks (never-worse). Only CALM-added work carries bounds.
	// The child's environment carries the AD07 sentinel so a nested
	// calm-capture exec inside it passes through instead of re-wrapping.
	r, runErr := runWrapped(ctx, argv, cwd, CaptureActiveEnv+"=1")
	if runErr != nil {
		_, _ = fmt.Fprintln(d.Stdout, "failed to run command:", runErr)
		return 1
	}

	engine := capture.NewEngine(d.Client, mgr, mgr, d.Logger, recallFor(hookBinPath(), sid), capture.WithDiscoveryCard())
	raw := capture.CommandPayload(r)
	out := engine.Capture(ctx, capture.Spec{
		Ingest:  raw,
		Visible: raw,
		Res:     r,
		Plan: func(seq int64) (extract.Plan, error) {
			return extract.DerivePlan(
				extract.Invocation{Seq: seq, Command: command, Cwd: cwd, WorkspaceRoot: cwd},
				extract.ExecResult{Stdout: r.Stdout, Stderr: r.Stderr, ExitCode: r.ExitCode, TimedOut: r.TimedOut},
			)
		},
	})

	// Response-first: the presentation bytes reach stdout before any flush
	// network I/O, so the agent's read never waits on delivery (DESIGN.md §9).
	_, _ = fmt.Fprint(d.Stdout, composeExec(out))

	d.drain(ctx, mgr)
	if d.gcSample() {
		if gerr := session.GC(d.Root, d.sessionTTL()); gerr != nil {
			d.Logger.WithContext(ctx).Warn("reclamation sweep failed", logging.ErrorField(gerr))
		}
	}

	// never-worse: capture state never alters the wrapped command's exit code.
	return r.ExitCode
}

// composeExec appends the recall trailer (or the degradation sentence when
// degraded) so even a small inline capture stays recall-discoverable.
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

// argvAssignmentGuard rejects argv-form invocations whose first word is an
// assignment prefix (`exec -- FOO=bar cmd`, multiple args): argv form would try
// to run `FOO=bar` as a program. It writes the single-string guidance to out
// and returns true when it fires. Single-string form (one arg) is unaffected —
// the shell applies the assignment and the engine strips it from the label.
func argvAssignmentGuard(argv []string, out io.Writer) bool {
	if len(argv) > 1 && extract.IsAssignmentPrefix(argv[0]) {
		_, _ = fmt.Fprintln(out, "calm-capture exec: assignment prefix needs the single-string form: exec -- 'FOO=bar cmd …'")
		return true
	}
	return false
}

// PassthroughExec is exec's bootstrap-failure floor: with no usable config,
// logger, or client, the wrapped command still runs and its output still
// returns — a broken CALM setup must never block the local action
// (never-worse). Output matches the engine's degraded shape: the raw payload
// plus the one capture_failed sentence. The underlying bootstrap error stays
// off both streams (capture surfaces); `init` is the operator's affordance
// for surfacing it.
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
	// The guard runs on the degraded floors too: an argv-form assignment must
	// get the single-string guidance even when capture is already down, never
	// the raw "executable file not found".
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

// passthroughRun runs the command with no capture: raw stdout/stderr and the exit
// code pass through verbatim.
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

// runWrapped executes the wrapped argv. A single argument is a pre-quoted
// shell string (the hook-rewrite form) and runs through the shell; several
// arguments are verbatim argv and run without one — joining them into a shell
// string would re-tokenize and corrupt quoting (`printf '%s\n' 'hello world'`
// becomes hellonworldn).
func runWrapped(ctx context.Context, argv []string, cwd string, extraEnv ...string) (exec.Result, error) {
	if len(argv) == 1 {
		return exec.Run(ctx, argv[0], cwd, extraEnv...)
	}
	return exec.RunArgv(ctx, argv, cwd, extraEnv...)
}
