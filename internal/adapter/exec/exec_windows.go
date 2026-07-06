// Copyright 2026 The CALM Authors
// SPDX-License-Identifier: Apache-2.0

//go:build windows

package exec

import osexec "os/exec"

// ShellName is the platform shell named in agent-facing tool descriptions;
// ShellInvocation is the exact invocation form those descriptions teach.
// cmd is the sh analog that always ships with the OS; PowerShell stays
// reachable through it (`powershell -Command …`) the way bash is through sh.
const (
	ShellName       = "cmd"
	ShellInvocation = "cmd /c"
)

func shellArgv(command string) []string {
	return []string{"cmd", "/c", command}
}

// Windows has no process groups: CommandContext's default Cancel kills only
// the direct child, so a forked grandchild can outlive the timeout kill;
// WaitDelay still bounds the wait. Lifting this (Job Objects) is future work
// if Windows runtime support formalizes.
func setupProcessControl(_ *osexec.Cmd) {}
