// Copyright 2026 The CALM Authors
// SPDX-License-Identifier: Apache-2.0

package mcp

import (
	"context"
	"encoding/json"
	"os"
	"strings"

	"github.com/one-harsh/calm/internal/adapter/exec"
	"github.com/one-harsh/calm/internal/adapter/extract"
)

const toolNameListDir = "calm_list_dir"

const listDirDescription = "List a workspace directory's entries (directories end with /), capturing the " +
	"listing into CALM. Prefer this over shell ls. Small listings come back verbatim (still captured and " +
	"searchable via calm_search). Larger listings come back as a compact summary plus a source label " +
	"ending in @<token>; fetch the full listing later with calm_search source=<label exactly as returned> " +
	"rather than re-listing. The label refers to the latest listing of this directory. Never append #<n> " +
	"after the @<token>. In multi-workspace sessions, set workspace=<id> to target a non-default workspace."

const listDirSchema = `{
  "type": "object",
  "properties": {
    "path": {"type": "string", "description": "Directory path, workspace-relative; defaults to the workspace root."},
    "workspace": {"type": "string", "description": "Workspace ID to target; defaults to the primary workspace."}
  },
  "additionalProperties": false
}`

type listDirArgs struct {
	Path      string `json:"path"`
	Workspace string `json:"workspace"`
}

func (s *Server) newListDirTool() Tool {
	return Tool{
		Name:        toolNameListDir,
		Description: listDirDescription,
		InputSchema: json.RawMessage(listDirSchema),
		Handler:     s.listDir,
		Annotations: readOnlyAnnotations,
	}
}

func (s *Server) listDir(ctx context.Context, args json.RawMessage) (ToolResult, error) {
	var a listDirArgs
	if uerr := json.Unmarshal(args, &a); uerr != nil {
		return ToolResult{}, &ArgError{Detail: uerr.Error()}
	}

	wb, werr := s.workspaceForPath(a.Workspace, a.Path)
	if werr != nil {
		return ToolResult{}, werr
	}
	dir := a.Path
	if dir == "" {
		dir = "."
	}
	entries, derr := os.ReadDir(wb.resolve(dir))
	if derr != nil {
		return TextResult("list failed: "+derr.Error(), true), nil
	}

	var b strings.Builder
	for _, e := range entries {
		b.WriteString(e.Name())
		if e.IsDir() {
			b.WriteString("/")
		}
		b.WriteString("\n")
	}
	listing := b.String()

	r := exec.Result{Stdout: listing}
	return s.capturePipeline(ctx, captureSpec{
		ingest:  listing,
		visible: listing,
		res:     r,
		plan: func() (extract.Plan, error) {
			return extract.PlanListDir(s.invocation(wb, "", wb.Root), execResultOf(r), a.Path), nil
		},
	})
}
