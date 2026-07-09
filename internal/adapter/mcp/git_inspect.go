// Copyright 2026 The CALM Authors
// SPDX-License-Identifier: Apache-2.0

package mcp

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/one-harsh/calm/internal/adapter/exec"
	"github.com/one-harsh/calm/internal/adapter/extract"
)

const (
	toolNameGitStatus = "calm_git_status"
	toolNameGitDiff   = "calm_git_diff"
)

const gitStatusDescription = "Inspect git working-tree and index state from the workspace root, capturing " +
	"the output into CALM. Prefer this over running git status through a shell. Small outputs come back " +
	"verbatim (still captured and searchable via calm_search). Larger outputs come back as a compact " +
	"summary plus a source label ending in @<token>; fetch the full output later with calm_search " +
	"source=<label exactly as returned>. The label refers to the latest status; for one specific past " +
	"snapshot, drop the @<token> suffix and use <base>#<n>. Never append #<n> after the @<token>. In " +
	"multi-workspace sessions, set workspace=<id> to target a non-default workspace."

const gitDiffDescription = "Inspect a git diff (optionally for specific refs and paths), capturing the " +
	"patch into CALM. Prefer this over running git diff through a shell. Small diffs come back verbatim " +
	"(still captured and searchable via calm_search). Larger diffs come back as a compact summary plus a " +
	"source label ending in @<token>; fetch the full patch later with calm_search " +
	"source=<label exactly as returned> rather than re-diffing. The label refers to the latest diff for " +
	"these refs; for one specific past snapshot, drop the @<token> suffix and use <base>#<n>. Never " +
	"append #<n> after the @<token>. In multi-workspace sessions, set workspace=<id> to target a " +
	"non-default workspace."

const gitStatusSchema = `{
  "type": "object",
  "properties": {
    "workspace": {"type": "string", "description": "Workspace ID to target; defaults to the primary workspace."}
  },
  "additionalProperties": false
}`

const gitDiffSchema = `{
  "type": "object",
  "properties": {
    "refs": {"type": "array", "items": {"type": "string"}, "description": "Revisions or ranges (e.g. HEAD~1, main..feat); defaults to the working tree against HEAD."},
    "paths": {"type": "array", "items": {"type": "string"}, "description": "Limit the diff to these workspace-relative paths."},
    "workspace": {"type": "string", "description": "Workspace ID to target; defaults to the primary workspace."}
  },
  "additionalProperties": false
}`

type gitStatusArgs struct {
	Workspace string `json:"workspace"`
}

type gitDiffArgs struct {
	Refs      []string `json:"refs"`
	Paths     []string `json:"paths"`
	Workspace string   `json:"workspace"`
}

func (s *Server) newGitStatusTool() Tool {
	return Tool{
		Name:        toolNameGitStatus,
		Description: gitStatusDescription,
		InputSchema: json.RawMessage(gitStatusSchema),
		Handler:     s.gitStatus,
		Annotations: readOnlyAnnotations,
	}
}

func (s *Server) newGitDiffTool() Tool {
	return Tool{
		Name:        toolNameGitDiff,
		Description: gitDiffDescription,
		InputSchema: json.RawMessage(gitDiffSchema),
		Handler:     s.gitDiff,
		Annotations: readOnlyAnnotations,
	}
}

func (s *Server) gitStatus(ctx context.Context, args json.RawMessage) (ToolResult, error) {
	var a gitStatusArgs
	if len(args) > 0 {
		if uerr := json.Unmarshal(args, &a); uerr != nil {
			return ToolResult{}, &ArgError{Detail: uerr.Error()}
		}
		// Schema strictness at the handler boundary: workspace is the only key.
		var m map[string]any
		if err := json.Unmarshal(args, &m); err == nil {
			delete(m, "workspace")
			if len(m) > 0 {
				return ToolResult{}, &ArgError{Detail: "this tool takes only the optional workspace argument"}
			}
		}
	}
	wb, werr := s.selectWorkspace(a.Workspace)
	if werr != nil {
		return ToolResult{}, werr
	}
	return s.runGitInspect(ctx, wb, []string{"git", "status"}, func(r exec.Result) func() (extract.Plan, error) {
		return func() (extract.Plan, error) {
			return extract.PlanGitStatus(s.invocation(wb, "", wb.Root), execResultOf(r)), nil
		}
	})
}

func (s *Server) gitDiff(ctx context.Context, args json.RawMessage) (ToolResult, error) {
	var a gitDiffArgs
	if len(args) > 0 {
		if uerr := json.Unmarshal(args, &a); uerr != nil {
			return ToolResult{}, &ArgError{Detail: uerr.Error()}
		}
	}
	for _, ref := range a.Refs {
		if strings.TrimSpace(ref) == "" {
			return ToolResult{}, &ArgError{Detail: "refs entries must be non-blank"}
		}
		// Flag-injection guard: revisions never start with '-'.
		if strings.HasPrefix(ref, "-") {
			return ToolResult{}, &ArgError{Detail: "invalid ref " + ref}
		}
	}
	for _, p := range a.Paths {
		if strings.TrimSpace(p) == "" {
			return ToolResult{}, &ArgError{Detail: "paths entries must be non-blank"}
		}
	}

	wb, werr := s.selectWorkspace(a.Workspace)
	if werr != nil {
		return ToolResult{}, werr
	}
	argv := append([]string{"git", "diff"}, a.Refs...)
	if len(a.Paths) > 0 {
		argv = append(argv, "--")
		argv = append(argv, a.Paths...)
	}
	return s.runGitInspect(ctx, wb, argv, func(r exec.Result) func() (extract.Plan, error) {
		return func() (extract.Plan, error) {
			return extract.PlanGitDiff(s.invocation(wb, "", wb.Root), execResultOf(r), a.Refs, a.Paths), nil
		}
	})
}

// runGitInspect mirrors calm_run_command's subprocess semantics: nonzero exit
// (not a repo, bad ref) still captures the output under the derived labels.
func (s *Server) runGitInspect(ctx context.Context, wb WorkspaceBinding, argv []string, planFor func(exec.Result) func() (extract.Plan, error)) (ToolResult, error) {
	ectx, cancel := context.WithTimeout(ctx, execTimeout)
	defer cancel()
	r, runErr := exec.RunArgv(ectx, argv, wb.Root)
	if runErr != nil {
		return TextResult("failed to run git: "+runErr.Error(), true), nil
	}
	raw := commandPayload(r)
	return s.capturePipeline(ctx, captureSpec{ingest: raw, visible: raw, res: r, plan: planFor(r)})
}
