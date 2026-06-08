// Copyright 2026 The CALM Authors
// SPDX-License-Identifier: Apache-2.0

package exec

import (
	"bytes"
	"context"
	"errors"
	osexec "os/exec"
	"syscall"
	"time"
)

const maxOutputBytes = 512 * 1024

const killGraceDelay = 2 * time.Second

type Result struct {
	Stdout    string
	Stderr    string
	ExitCode  int
	TimedOut  bool
	Truncated bool
}

func Run(ctx context.Context, command, dir string) (Result, error) {
	//nolint:gosec // DL02: local exec is an adapter capability; CALM the service never runs code
	cmd := osexec.CommandContext(ctx, "sh", "-c", command)
	cmd.Dir = dir

	// Own process group + kill the whole group on timeout. Killing only the sh leader
	// leaves a forked child (e.g. `sleep`) holding the inherited output pipe, which keeps
	// Wait blocked until that child exits — long past the deadline.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		return nil
	}
	cmd.WaitDelay = killGraceDelay

	stdout := &cappedBuffer{limit: maxOutputBytes}
	stderr := &cappedBuffer{limit: maxOutputBytes}
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
