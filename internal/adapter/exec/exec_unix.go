// Copyright 2026 The CALM Authors
// SPDX-License-Identifier: Apache-2.0

//go:build unix

package exec

import (
	osexec "os/exec"
	"syscall"
)

// ShellName is the platform shell named in agent-facing tool descriptions;
// ShellInvocation is the exact invocation form those descriptions teach.
const (
	ShellName       = "sh"
	ShellInvocation = "sh -c"
)

func shellArgv(command string) []string {
	return []string{"sh", "-c", command}
}

// Own process group + kill the whole group on timeout. Killing only the sh
// leader leaves a forked child (e.g. `sleep`) holding the inherited output
// pipe, which keeps Wait blocked until that child exits — long past the
// deadline.
func setupProcessControl(cmd *osexec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		return nil
	}
}
