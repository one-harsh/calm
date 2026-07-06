// Copyright 2026 The CALM Authors
// SPDX-License-Identifier: Apache-2.0

package extract

import "testing"

// swapRegistry installs the registry for another GOOS for one test. Serial
// only — it mutates the package var the classify path reads.
func swapRegistry(t *testing.T, goos string) {
	t.Helper()
	prev := registry
	registry = buildRegistry(goos)
	t.Cleanup(func() { registry = prev })
}

// Windows cmd-native idioms derive the same stable identities as their Unix
// equivalents, so agents writing `type`/`dir`/`findstr` through `cmd /c`
// keep replace-mode labels instead of falling to coexist.
func TestWindowsRegistry_CmdIdiomsGetStableLabels(t *testing.T) {
	swapRegistry(t, "windows")
	winInv := func(command string) Invocation {
		return Invocation{Seq: 1, Command: command, Cwd: "/ws", WorkspaceRoot: "/ws"}
	}

	p, err := DerivePlan(winInv("type foo.py"), ExecResult{})
	if err != nil || p.Mode != Replace || p.LatestSource != "calm:v1:file:read:foo.py" {
		t.Errorf("type = %+v (err %v); want replace file:read:foo.py", p, err)
	}

	p, _ = DerivePlan(winInv("dir src"), ExecResult{})
	if p.Mode != Replace || p.LatestSource != "calm:v1:file:list:src" {
		t.Errorf("dir = %+v; want replace file:list:src", p)
	}

	p, _ = DerivePlan(winInv("findstr TODO src"), ExecResult{})
	if p.Mode != Replace || p.LatestSource != "calm:v1:search:grep:TODO:src" {
		t.Errorf("findstr = %+v; want replace search:grep:TODO:src", p)
	}

	// cmd-style /flags resolve as escaping absolute paths — the whole command
	// falls to coexist, the same protective direction as output-affecting flags.
	p, _ = DerivePlan(winInv("dir /s src"), ExecResult{})
	if p.Mode != Coexist {
		t.Errorf("dir /s = %+v; want coexist (flag-shaped operand)", p)
	}
}

// The aliases are per-platform: on unix a program named `type` or `dir` is
// just an unrecognized executable and must not alias with read/list labels.
func TestUnixRegistry_NoWindowsAliases(t *testing.T) {
	swapRegistry(t, "linux")
	inv := Invocation{Seq: 1, Command: "dir src", Cwd: "/ws", WorkspaceRoot: "/ws"}
	p, _ := DerivePlan(inv, ExecResult{})
	if p.Mode != Coexist || p.HistorySource != "calm:v1:shell:dir#1" {
		t.Errorf("unix dir = %+v; want coexist shell:dir#1", p)
	}
}
