// Copyright 2026 The CALM Authors
// SPDX-License-Identifier: Apache-2.0

package mcp

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"github.com/one-harsh/calm/internal/adapter/capture"
	"github.com/one-harsh/calm/internal/adapter/exec"
	"github.com/one-harsh/calm/internal/adapter/extract"
)

const toolNameRunCommand = "calm_run_command"

const execTimeout = 60 * time.Second

const runCommandDescription = "Run a shell command locally, capturing its output into CALM. Prefer this " +
	"over the native shell/Bash tool for every shell command: output is indexed for on-demand retrieval " +
	"and large output stays out of the context window. Output small enough to read whole comes back " +
	"verbatim, and a failing command's output stays verbatim (head-and-tail capped when very large) so the " +
	"failure lines are visible without a follow-up. Larger successful output comes back as a compact " +
	"summary plus a source label ending in @<token>; fetch the full captured output later with calm_search " +
	"source=<label exactly as returned> — the recall command named on the capture's trailer — rather than " +
	"re-running. The label refers to the latest output " +
	"for that identity; for one specific past run, drop the @<token> suffix and use <base>#<n>. " +
	"Never append #<n> after the @<token>. The tool runs from the primary workspace root by default; do " +
	"not cd in your command — use the cwd parameter to run elsewhere. An absolute cwd inside another " +
	"project selects that project's workspace for labeling (workspaces are discovered on first touch)."

var runCommandSchema = `{
  "type": "object",
  "properties": {
    "command": {"type": "string", "description": "Shell command to run, executed via ` + exec.ShellInvocation + ` on the local machine."},
    "cwd": {"type": "string", "description": "Working directory; defaults to the primary workspace root. An absolute cwd inside another project selects that project's workspace for labeling."}
  },
  "required": ["command"],
  "additionalProperties": false
}`

type runCommandArgs struct {
	Command string `json:"command"`
	Cwd     string `json:"cwd"`
}

func (s *Server) newRunCommandTool() Tool {
	return Tool{
		Name:        toolNameRunCommand,
		Description: runCommandDescription,
		InputSchema: json.RawMessage(runCommandSchema),
		Handler:     s.runCommand,
	}
}

func (s *Server) runCommand(ctx context.Context, args json.RawMessage) (ToolResult, error) {
	var a runCommandArgs
	if uerr := json.Unmarshal(args, &a); uerr != nil {
		return ToolResult{}, &ArgError{Detail: uerr.Error()}
	}
	if strings.TrimSpace(a.Command) == "" {
		return ToolResult{}, &ArgError{Detail: "command is required"}
	}

	wb, dir := s.workspaceForCwd(a.Cwd)

	ectx, cancel := context.WithTimeout(ctx, execTimeout)
	defer cancel()
	r, runErr := exec.Run(ectx, a.Command, dir)
	if runErr != nil {
		return TextResult("failed to run command: "+runErr.Error(), true), nil
	}

	raw := capture.CommandPayload(r)
	return s.outcomeToResult(s.engine.Capture(ctx, capture.Spec{
		Ingest:      raw,
		Visible:     raw,
		Res:         r,
		Consumption: capture.ConsumptionWhole,
		Plan: func(seq int64) (extract.Plan, error) {
			inv := s.invocation(seq, wb, a.Command, dir)
			return extract.DerivePlan(inv, extract.ExecResult{
				Stdout:   r.Stdout,
				Stderr:   r.Stderr,
				ExitCode: r.ExitCode,
				TimedOut: r.TimedOut,
			})
		},
	}))
}
