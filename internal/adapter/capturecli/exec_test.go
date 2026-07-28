// Copyright 2026 The CALM Authors
// SPDX-License-Identifier: Apache-2.0

package capturecli

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/mock"

	"github.com/one-harsh/calm/internal/adapter/calm"
	"github.com/one-harsh/calm/internal/adapter/capture"
	"github.com/one-harsh/calm/internal/adapter/obs"
)

const cardMarker = "CALM keeps captured output searchable"

// exec's happy path prints the engine presentation to stdout, keeps process
// stderr empty (invariant 7), and propagates the wrapped command's exit code.
func TestExec_Happy_PresentationToStdoutStderrPure(t *testing.T) {
	c := calm.NewMockClient(t)
	expectEstablish(c, "tok1")
	d, stdout, stderr := newDeps(t, c)

	code := Dispatch(context.Background(), d, execArgs("conv", "printf hello"))

	if code != 0 {
		t.Fatalf("exit = %d; want 0", code)
	}
	if !strings.HasPrefix(stdout.String(), "hello") {
		t.Errorf("stdout must lead with the raw output; got:\n%s", stdout.String())
	}
	if stderr.Len() != 0 {
		t.Errorf("exec must keep process stderr empty; got:\n%s", stderr.String())
	}
}

// A non-degraded capture — even a small inline one that prints no summary line —
// carries a trailer with the fused source label (recall) and the feedback ref
// (outcome reporting), resolving F1.
func TestExec_Trailer_LabelAndFeedbackRef(t *testing.T) {
	c := calm.NewMockClient(t)
	c.EXPECT().RegisterClient(mock.Anything, mock.Anything).Return(true, nil).Maybe()
	c.EXPECT().CreateSession(mock.Anything, mock.Anything, mock.Anything, mock.Anything).Return("tok1", nil).Once()
	c.EXPECT().Ingest(mock.Anything, "tok1", mock.Anything).
		Return(calm.IngestSummary{Source: "calm:v1:test", SectionsIndexed: 1, SectionsTotal: 1, CorrelationID: "corr-9"}, nil).Maybe()
	c.EXPECT().WriteEvents(mock.Anything, "tok1", mock.Anything).Return(nil).Maybe()
	d, stdout, _ := newDeps(t, c)

	if code := Dispatch(context.Background(), d, execArgs("conv", "printf hello")); code != 0 {
		t.Fatalf("exit = %d; want 0", code)
	}
	out := stdout.String()
	if !strings.Contains(out, "↳ source=calm:v1:test@") {
		t.Errorf("trailer must carry the fused source label; got:\n%s", out)
	}
	if !strings.Contains(out, "feedback: calm-capture feedback corr-9") {
		t.Errorf("trailer must carry the feedback ref; got:\n%s", out)
	}
}

func TestExec_NonzeroExitPropagates(t *testing.T) {
	c := calm.NewMockClient(t)
	expectEstablish(c, "tok1")
	d, stdout, _ := newDeps(t, c)

	code := Dispatch(context.Background(), d, execArgs("conv", "echo out; exit 3"))

	if code != 3 {
		t.Fatalf("exit = %d; want 3 (verbatim)", code)
	}
	if !strings.Contains(stdout.String(), "out") {
		t.Errorf("stdout must carry the command output; got:\n%s", stdout.String())
	}
}

// CALM down: the local output is shown verbatim followed by the one canonical
// degradation sentence, the exit code is still the wrapped command's, and no
// session is created (transient register failure never latches).
func TestExec_Degraded_RawPlusCanonicalSentence(t *testing.T) {
	c := calm.NewMockClient(t)
	c.EXPECT().RegisterClient(mock.Anything, mock.Anything).
		Return(false, &calm.StatusError{Op: "register", Code: 503, Status: "503 unavailable"}).Once()
	d, stdout, stderr := newDeps(t, c)

	code := Dispatch(context.Background(), d, execArgs("conv", "printf hello"))

	if code != 0 {
		t.Fatalf("exit = %d; want 0 (capture failure never alters exit code)", code)
	}
	want := "hello\n" + obs.DegradedPhrase(obs.DegradedReasonCalmUnreachable) + "\n"
	if stdout.String() != want {
		t.Errorf("stdout = %q; want raw + one canonical sentence %q", stdout.String(), want)
	}
	if stderr.Len() != 0 {
		t.Errorf("exec must keep process stderr empty; got:\n%s", stderr.String())
	}
}

// AD07: the wrapped command's environment carries CALM_CAPTURE_ACTIVE=1 so a
// nested calm-capture would pass through.
func TestExec_SetsReentrancySentinelInChildEnv(t *testing.T) {
	t.Setenv(CaptureActiveEnv, "")
	c := calm.NewMockClient(t)
	expectEstablish(c, "tok1")
	d, stdout, _ := newDeps(t, c)

	code := Dispatch(context.Background(), d, execArgs("conv", `printf '%s' "$CALM_CAPTURE_ACTIVE"`))

	if code != 0 {
		t.Fatalf("exit = %d; want 0", code)
	}
	if !strings.HasPrefix(stdout.String(), "1") {
		t.Errorf("child must see CALM_CAPTURE_ACTIVE=1; stdout:\n%s", stdout.String())
	}
}

// Response-first: the presentation reaches stdout before the event flush's
// network write (never-worse — the read never waits on delivery).
func TestExec_PresentationWrittenBeforeEventFlush(t *testing.T) {
	c := calm.NewMockClient(t)
	c.EXPECT().RegisterClient(mock.Anything, mock.Anything).Return(true, nil).Maybe()
	c.EXPECT().CreateSession(mock.Anything, mock.Anything, mock.Anything, mock.Anything).Return("tok1", nil).Once()
	c.EXPECT().Ingest(mock.Anything, "tok1", mock.Anything).
		Return(calm.IngestSummary{Source: "calm:v1:test", SectionsIndexed: 1, SectionsTotal: 1}, nil).Maybe()

	d, stdout, _ := newDeps(t, c)
	stdoutBeforeFlush := -1
	c.EXPECT().WriteEvents(mock.Anything, "tok1", mock.Anything).Run(func(_ context.Context, _ string, _ []calm.EventInput) {
		stdoutBeforeFlush = stdout.Len()
	}).Return(nil).Once()

	Dispatch(context.Background(), d, execArgs("conv", "printf hello"))

	if stdoutBeforeFlush <= 0 {
		t.Errorf("presentation must be on stdout before the event flush; stdout length at flush = %d", stdoutBeforeFlush)
	}
}

func TestExec_DiscoveryCardOnFirstCaptureOnly(t *testing.T) {
	c := calm.NewMockClient(t)
	expectEstablish(c, "tok1")
	d, stdout, _ := newDeps(t, c)

	Dispatch(context.Background(), d, execArgs("conv", "printf hello"))
	first := stdout.String()
	stdout.Reset()
	Dispatch(context.Background(), d, execArgs("conv", "printf world"))
	second := stdout.String()

	if !strings.Contains(first, cardMarker) {
		t.Errorf("first capture must carry the discovery card; got:\n%s", first)
	}
	if strings.Contains(second, cardMarker) {
		t.Errorf("second capture must not carry the discovery card; got:\n%s", second)
	}
}

func TestExec_GCSampleReapsIdleDir(t *testing.T) {
	c := calm.NewMockClient(t)
	expectEstablish(c, "tok1")
	d, _, _ := newDeps(t, c)
	d.Cfg.Calm.GCSampleRate = 1

	oldDir := filepath.Join(d.Root, "sessions", "oldconv")
	if err := os.MkdirAll(oldDir, 0o700); err != nil {
		t.Fatal(err)
	}
	stale := time.Now().Add(-72 * time.Hour)
	if err := os.Chtimes(oldDir, stale, stale); err != nil {
		t.Fatal(err)
	}

	Dispatch(context.Background(), d, execArgs("conv", "printf hello"))

	if _, err := os.Stat(oldDir); !os.IsNotExist(err) {
		t.Errorf("idle conversation dir must be reaped when the GC sample fires; stat err = %v", err)
	}
	if _, err := os.Stat(filepath.Join(d.Root, "sessions", "conv")); err != nil {
		t.Errorf("the fresh conversation dir must survive reclamation; stat err = %v", err)
	}
}

// exec with nothing after -- is a usage error surfaced on stdout (stderr stays
// pure), and no session is established.
func TestExec_NoCommand(t *testing.T) {
	c := calm.NewMockClient(t) // strict: no CALM call
	d, stdout, stderr := newDeps(t, c)

	code := Dispatch(context.Background(), d, []string{"exec", "--session", "conv", "--"})

	if code != 2 || !strings.Contains(stdout.String(), "no command") || stderr.Len() != 0 {
		t.Errorf("exec no-command: exit=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
}

func TestExec_BadFlag(t *testing.T) {
	c := calm.NewMockClient(t) // strict: no CALM call
	d, _, stderr := newDeps(t, c)

	code := Dispatch(context.Background(), d, []string{"exec", "--nope", "--", "echo hi"})

	if code != 2 || stderr.Len() != 0 {
		t.Errorf("exec bad flag: exit=%d stderr=%q; want 2 and pure stderr", code, stderr.String())
	}
}

// AD07 (risk 8): a nested exec under the sentinel passes through — no capture,
// no card, no sentence, raw streams split verbatim; the strict mock proves no
// CALM traffic and no session establishment.
func TestExec_SentinelTriggersPassthrough(t *testing.T) {
	c := calm.NewMockClient(t) // strict: any call fails the test
	d, stdout, stderr := newDeps(t, c)
	t.Setenv(CaptureActiveEnv, "1")

	code := Dispatch(context.Background(), d, []string{"exec", "--", "echo inner; echo warn >&2"})
	if code != 0 {
		t.Fatalf("exit = %d; want 0", code)
	}
	if got := stdout.String(); got != "inner\n" {
		t.Errorf("stdout = %q; want raw command output only", got)
	}
	if got := stderr.String(); got != "warn\n" {
		t.Errorf("stderr = %q; want raw command stderr passed through", got)
	}
}

// Multi-word argv after -- runs without a shell, so quoting survives verbatim —
// a joined shell string would corrupt `printf '%s\n' 'hello world'` into
// hellonworldn.
func TestExec_MultiWordArgvRunsWithoutShell(t *testing.T) {
	c := calm.NewMockClient(t)
	expectEstablish(c, "tok1")
	d, stdout, _ := newDeps(t, c)

	code := Dispatch(context.Background(), d, []string{"exec", "--session", "conv", "--", "printf", "%s\n", "hello world"})
	if code != 0 {
		t.Fatalf("exit = %d; want 0", code)
	}
	if !strings.HasPrefix(stdout.String(), "hello world\n") {
		t.Errorf("stdout = %q; want quoting preserved verbatim", stdout.String())
	}
}

// PassthroughExec is the bootstrap-failure floor: with no config, logger, or
// client at all, the wrapped command still runs, its exit code propagates
// verbatim, and stdout carries the raw payload plus the capture_failed
// sentence (never-worse — a broken CALM setup must never block the action).
func TestPassthroughExec_RunsCommandWithoutBootstrap(t *testing.T) {
	t.Setenv(CaptureActiveEnv, "")
	stdout := &bytes.Buffer{}
	code := PassthroughExec(context.Background(), []string{"--", "echo", "floor-ok"}, stdout, &bytes.Buffer{})
	if code != 0 {
		t.Fatalf("exit = %d; want 0", code)
	}
	out := stdout.String()
	if !strings.HasPrefix(out, "floor-ok\n") {
		t.Errorf("raw payload missing from stdout: %q", out)
	}
	if !strings.Contains(out, obs.DegradedPhrase(obs.DegradedReasonCaptureFailed)) {
		t.Errorf("capture_failed sentence missing: %q", out)
	}
}

func TestPassthroughExec_NonzeroExitAndUsage(t *testing.T) {
	t.Setenv(CaptureActiveEnv, "")
	stdout := &bytes.Buffer{}
	if code := PassthroughExec(context.Background(), []string{"--", "exit 4"}, stdout, &bytes.Buffer{}); code != 4 {
		t.Errorf("exit = %d; want 4 verbatim", code)
	}
	if code := PassthroughExec(context.Background(), nil, &bytes.Buffer{}, &bytes.Buffer{}); code != 2 {
		t.Errorf("usage exit = %d; want 2", code)
	}
}

// Under the sentinel even the bootstrap floor stays transparent: raw output
// only, no capture_failed sentence — the ancestor owns presentation (AD07).
func TestPassthroughExec_SentinelSuppressesSentence(t *testing.T) {
	t.Setenv(CaptureActiveEnv, "1")
	stdout := &bytes.Buffer{}
	if code := PassthroughExec(context.Background(), []string{"--", "echo", "quiet"}, stdout, &bytes.Buffer{}); code != 0 {
		t.Fatalf("exit = %d; want 0", code)
	}
	if got := stdout.String(); got != "quiet\n" {
		t.Errorf("stdout = %q; want raw output with no degradation sentence", got)
	}
}

func TestComposeExec(t *testing.T) {
	if got := composeExec(capture.Outcome{Visible: "raw"}); got != "raw" {
		t.Errorf("no degradation: got %q; want the visible output verbatim", got)
	}
	got := composeExec(capture.Outcome{Visible: "raw", Reason: obs.DegradedReasonCaptureFailed})
	want := "raw\n" + obs.DegradedPhrase(obs.DegradedReasonCaptureFailed) + "\n"
	if got != want {
		t.Errorf("degraded: got %q; want %q", got, want)
	}
}

// A command that can't start (nonexistent binary, multi-word argv so it runs
// without a shell) never reaches CALM: execCmd returns 1 with the failure on
// stdout, and the strict mock proves no session was touched — the failure is
// the command's, not CALM's.
func TestExec_CommandStartFailure(t *testing.T) {
	c := calm.NewMockClient(t) // strict: no CALM call
	d, stdout, stderr := newDeps(t, c)

	code := Dispatch(context.Background(), d,
		[]string{"exec", "--session", "conv", "--", "/nonexistent/calm-capture-nope", "x"})

	if code != 1 {
		t.Fatalf("exit = %d; want 1 on a start failure", code)
	}
	if !strings.Contains(stdout.String(), "failed to run command") || stderr.Len() != 0 {
		t.Errorf("start failure: stdout=%q stderr=%q", stdout.String(), stderr.String())
	}
}

// The bootstrap-failure floor survives a start failure too: exit 1, failure on
// stdout, and no client needed at all.
func TestPassthroughExec_StartFailure(t *testing.T) {
	t.Setenv(CaptureActiveEnv, "")
	stdout := &bytes.Buffer{}
	code := PassthroughExec(context.Background(),
		[]string{"--", "/nonexistent/calm-capture-nope", "x"}, stdout, &bytes.Buffer{})
	if code != 1 {
		t.Fatalf("exit = %d; want 1 on a start failure", code)
	}
	if !strings.Contains(stdout.String(), "failed to run command") {
		t.Errorf("stdout = %q; want the failure notice", stdout.String())
	}
}

// Under the sentinel the transparent passthrough surfaces a start failure the
// same way — exit 1, failure notice, no CALM traffic.
func TestExec_SentinelPassthrough_StartFailure(t *testing.T) {
	c := calm.NewMockClient(t) // strict: any call fails the test
	d, stdout, _ := newDeps(t, c)
	t.Setenv(CaptureActiveEnv, "1")

	code := Dispatch(context.Background(), d,
		[]string{"exec", "--", "/nonexistent/calm-capture-nope", "x"})
	if code != 1 {
		t.Fatalf("exit = %d; want 1 on a start failure", code)
	}
	if !strings.Contains(stdout.String(), "failed to run command") {
		t.Errorf("stdout = %q; want the failure notice", stdout.String())
	}
}
