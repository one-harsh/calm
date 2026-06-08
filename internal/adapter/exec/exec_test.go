// Copyright 2026 The CALM Authors
// SPDX-License-Identifier: Apache-2.0

package exec_test

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/one-harsh/calm/internal/adapter/exec"
)

func TestRun_CapturesStdoutAndExitZero(t *testing.T) {
	res, err := exec.Run(context.Background(), "echo hi", "")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Stdout != "hi\n" {
		t.Errorf("stdout = %q; want %q", res.Stdout, "hi\n")
	}
	if res.ExitCode != 0 {
		t.Errorf("exit = %d; want 0", res.ExitCode)
	}
	if res.TimedOut || res.Truncated {
		t.Errorf("unexpected flags: timedOut=%v truncated=%v", res.TimedOut, res.Truncated)
	}
}

func TestRun_NonZeroExitIsResultNotError(t *testing.T) {
	res, err := exec.Run(context.Background(), "exit 3", "")
	if err != nil {
		t.Fatalf("Run returned error for a non-zero exit; want result: %v", err)
	}
	if res.ExitCode != 3 {
		t.Errorf("exit = %d; want 3", res.ExitCode)
	}
}

func TestRun_CapturesStderr(t *testing.T) {
	res, err := exec.Run(context.Background(), "echo oops 1>&2", "")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Stderr != "oops\n" {
		t.Errorf("stderr = %q; want %q", res.Stderr, "oops\n")
	}
}

func TestRun_TimeoutDoesNotHang(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	// The subshell forces sh to fork a child that inherits the output pipe. Killing
	// only the sh leader would leave that child holding the pipe and block Wait for the
	// full sleep; the process-group kill must take down the whole tree promptly.
	start := time.Now()
	res, err := exec.Run(ctx, "(sleep 5)", "")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if elapsed := time.Since(start); elapsed > 4*time.Second {
		t.Fatalf("Run took %v; deadline should have killed the process group promptly", elapsed)
	}
	if !res.TimedOut {
		t.Errorf("TimedOut = false; want true")
	}
}

func TestRun_HonorsWorkingDirectory(t *testing.T) {
	dir := t.TempDir()
	res, err := exec.Run(context.Background(), "pwd", dir)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	got, err := filepath.EvalSymlinks(trimNewline(res.Stdout))
	if err != nil {
		t.Fatalf("EvalSymlinks(%q): %v", res.Stdout, err)
	}
	want, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatalf("EvalSymlinks(%q): %v", dir, err)
	}
	if got != want {
		t.Errorf("pwd = %q; want %q", got, want)
	}
}

func TestRun_OversizedOutputIsTruncated(t *testing.T) {
	// head -c bounds the producer above the cap, so the result is deterministic.
	res, err := exec.Run(context.Background(), "yes a | head -c 1000000", "")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !res.Truncated {
		t.Errorf("Truncated = false; want true for >512KiB output")
	}
	if len(res.Stdout) != 512*1024 {
		t.Errorf("captured %d bytes; want cap of %d", len(res.Stdout), 512*1024)
	}
}

func TestRun_StartFailureReturnsError(t *testing.T) {
	// A non-existent dir fails before start — the one case Run returns an error.
	_, err := exec.Run(context.Background(), "echo hi", "/no/such/dir/zzz-does-not-exist")
	if err == nil {
		t.Fatal("Run with an invalid dir should return a start error")
	}
}

func TestRun_AlreadyExpiredContextIsTimedOut(t *testing.T) {
	ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancel()
	res, err := exec.Run(ctx, "echo hi", "")
	if err != nil {
		t.Fatalf("an expired deadline is a timed-out result, not an error: %v", err)
	}
	if !res.TimedOut {
		t.Errorf("TimedOut = false; want true for an already-expired context")
	}
}

func trimNewline(s string) string {
	if len(s) > 0 && s[len(s)-1] == '\n' {
		return s[:len(s)-1]
	}
	return s
}
