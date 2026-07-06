// Copyright 2026 The CALM Authors
// SPDX-License-Identifier: Apache-2.0

package exec

import (
	"bytes"
	"context"
	"errors"
	osexec "os/exec"
	"time"
)

// MaxOutputBytes caps each captured stream. Shared with native capture paths
// (structured file reads) so subprocess and native captures cannot diverge.
const MaxOutputBytes = 512 * 1024

const killGraceDelay = 2 * time.Second

type Result struct {
	Stdout    string
	Stderr    string
	ExitCode  int
	TimedOut  bool
	Truncated bool
}

func Run(ctx context.Context, command, dir string) (Result, error) {
	argv := shellArgv(command)
	//nolint:gosec // DL02: local exec is an adapter capability; CALM the service never runs code
	cmd := osexec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.Dir = dir
	return runCmd(ctx, cmd)
}

// RunArgv executes argv directly — no shell. Typed tool arguments must never
// be spliced into a shell string, so this is the only sanctioned exec entry
// for structured tools.
func RunArgv(ctx context.Context, argv []string, dir string) (Result, error) {
	if len(argv) == 0 {
		return Result{}, errors.New("exec: empty argv")
	}
	//nolint:gosec // DL02: local exec is an adapter capability; CALM the service never runs code
	cmd := osexec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.Dir = dir
	return runCmd(ctx, cmd)
}

func runCmd(ctx context.Context, cmd *osexec.Cmd) (Result, error) {
	setupProcessControl(cmd)
	cmd.WaitDelay = killGraceDelay

	stdout := &cappedBuffer{limit: MaxOutputBytes}
	stderr := &cappedBuffer{limit: MaxOutputBytes}
	cmd.Stdout = stdout
	cmd.Stderr = stderr

	runErr := cmd.Run()
	res := Result{
		Stdout:    stdout.String(),
		Stderr:    stderr.String(),
		Truncated: stdout.truncated || stderr.truncated,
		TimedOut:  errors.Is(ctx.Err(), context.DeadlineExceeded),
	}
	if runErr == nil {
		return res, nil
	}

	// A deadline kill arrives as *ExitError too — a command that ran, not a start failure.
	var exitErr *osexec.ExitError
	if errors.As(runErr, &exitErr) {
		res.ExitCode = exitErr.ExitCode()
		return res, nil
	}
	if res.TimedOut {
		res.ExitCode = -1
		return res, nil
	}
	return Result{}, runErr
}

// cappedBuffer keeps the first limit bytes and discards the rest, always reporting
// a full write so the child never blocks on a full pipe.
type cappedBuffer struct {
	buf       bytes.Buffer
	limit     int
	truncated bool
}

func (c *cappedBuffer) Write(p []byte) (int, error) {
	if remaining := c.limit - c.buf.Len(); remaining > 0 {
		if len(p) > remaining {
			c.buf.Write(p[:remaining])
			c.truncated = true
		} else {
			c.buf.Write(p)
		}
	} else if len(p) > 0 {
		c.truncated = true
	}
	return len(p), nil
}

func (c *cappedBuffer) String() string { return c.buf.String() }
