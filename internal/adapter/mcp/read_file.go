// Copyright 2026 The CALM Authors
// SPDX-License-Identifier: Apache-2.0

package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/one-harsh/calm/internal/adapter/capture"
	"github.com/one-harsh/calm/internal/adapter/exec"
	"github.com/one-harsh/calm/internal/adapter/extract"
)

const toolNameReadFile = "calm_read_file"

const readFileDescription = "Read a file from the workspace, capturing its content into CALM. Prefer this " +
	"over cat/head/tail through a shell: the whole file is indexed for on-demand retrieval. Small files " +
	"come back verbatim (still captured and searchable via calm_search). Larger files come back as a " +
	"compact summary plus a source label ending in @<token>; fetch the captured content later with " +
	"calm_search source=<label exactly as returned> rather than re-reading. Optional start_line/end_line " +
	"limit only what is shown — a scoped range comes back verbatim (capped past a size ceiling), while the " +
	"capture is always the full file. The label refers to the latest read " +
	"of this file, and it is the `basis` that calm_edit_file / calm_write_file require to mutate this path — " +
	"read once, then chain each mutation's returned label into the next. " +
	"Never append #<n> after the @<token>. In multi-workspace sessions, set " +
	"workspace=<id> to target a non-default workspace."

const readFileSchema = `{
  "type": "object",
  "properties": {
    "path": {"type": "string", "description": "File path, workspace-relative (absolute paths inside the workspace also resolve)."},
    "start_line": {"type": "integer", "description": "1-based first line to show; limits presentation only."},
    "end_line": {"type": "integer", "description": "1-based last line to show (inclusive); limits presentation only."},
    "workspace": {"type": "string", "description": "Workspace ID to target; defaults to the primary workspace."}
  },
  "required": ["path"],
  "additionalProperties": false
}`

type readFileArgs struct {
	Path      string `json:"path"`
	StartLine int    `json:"start_line"`
	EndLine   int    `json:"end_line"`
	Workspace string `json:"workspace"`
}

func (s *Server) newReadFileTool() Tool {
	return Tool{
		Name:        toolNameReadFile,
		Description: readFileDescription,
		InputSchema: json.RawMessage(readFileSchema),
		Handler:     s.readFile,
		Annotations: readOnlyAnnotations,
	}
}

func (s *Server) readFile(ctx context.Context, args json.RawMessage) (ToolResult, error) {
	var a readFileArgs
	if uerr := json.Unmarshal(args, &a); uerr != nil {
		return ToolResult{}, &ArgError{Detail: uerr.Error()}
	}
	if strings.TrimSpace(a.Path) == "" {
		return ToolResult{}, &ArgError{Detail: "path is required"}
	}
	if a.StartLine < 0 || a.EndLine < 0 {
		return ToolResult{}, &ArgError{Detail: "start_line/end_line must be positive"}
	}
	if a.StartLine > 0 && a.EndLine > 0 && a.EndLine < a.StartLine {
		return ToolResult{}, &ArgError{Detail: fmt.Sprintf("end_line %d is before start_line %d", a.EndLine, a.StartLine)}
	}

	b, werr := s.workspaceForPath(a.Workspace, a.Path)
	if werr != nil {
		return ToolResult{}, werr
	}

	abs := b.resolve(a.Path)
	full, truncated, rerr := readCapped(abs)
	if rerr != nil {
		return TextResult("read failed: "+rerr.Error(), true), nil
	}

	visible, ok := sliceLines(full, a.StartLine, a.EndLine)
	if !ok {
		return TextResult(fmt.Sprintf("read failed: start_line %d is past the end of the file (%d lines)",
			a.StartLine, strings.Count(full, "\n")+1), true), nil
	}

	r := exec.Result{Stdout: full, Truncated: truncated}
	out := s.engine.Capture(ctx, capture.Spec{
		Ingest:     full,
		Visible:    visible,
		Res:        r,
		RangedView: a.StartLine > 0 || a.EndLine > 0,
		Plan: func(seq int64) (extract.Plan, error) {
			return extract.PlanFileRead(s.invocation(seq, b, "", b.Root), execResultOf(r), a.Path), nil
		},
	})
	// A truncated read captured less than the file holds, so its label cannot
	// assert knowledge of the current bytes and must never become a basis.
	if !truncated {
		s.basis.Record(out.Label, abs, full)
	}
	return s.outcomeToResult(out)
}

// readCapped reads at most exec.MaxOutputBytes — the shared cap keeps native
// and subprocess captures from diverging.
func readCapped(path string) (content string, truncated bool, err error) {
	//nolint:gosec // DL02: local file access is an adapter capability; the workspace boundary is labeling-only, not access control (LABELING.md §4)
	f, err := os.Open(path)
	if err != nil {
		return "", false, err
	}
	defer func() { _ = f.Close() }()
	b, err := io.ReadAll(io.LimitReader(f, exec.MaxOutputBytes+1))
	if err != nil {
		return "", false, err
	}
	if len(b) > exec.MaxOutputBytes {
		return string(b[:exec.MaxOutputBytes]), true, nil
	}
	return string(b), false, nil
}

// sliceLines returns the 1-based inclusive [start, end] line range; zero
// values mean "from the beginning" / "to the end". ok=false when start lies
// past the last line.
func sliceLines(content string, start, end int) (string, bool) {
	if start <= 1 && end == 0 {
		return content, true
	}
	lines := strings.SplitAfter(content, "\n")
	if n := len(lines); n > 0 && lines[n-1] == "" {
		lines = lines[:n-1]
	}
	if start == 0 {
		start = 1
	}
	if start > len(lines) {
		return "", false
	}
	if end == 0 || end > len(lines) {
		end = len(lines)
	}
	return strings.Join(lines[start-1:end], ""), true
}

func execResultOf(r exec.Result) extract.ExecResult {
	return extract.ExecResult{Stdout: r.Stdout, Stderr: r.Stderr, ExitCode: r.ExitCode, TimedOut: r.TimedOut}
}
