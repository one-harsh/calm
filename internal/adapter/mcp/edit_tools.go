// Copyright 2026 The CALM Authors
// SPDX-License-Identifier: Apache-2.0

package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/one-harsh/calm/internal/adapter/exec"
	"github.com/one-harsh/calm/internal/adapter/extract"
)

const (
	toolNameEditFile  = "calm_edit_file"
	toolNameWriteFile = "calm_write_file"
)

const editFileDescription = "Edit a workspace file by replacing exactly one occurrence of old_string " +
	"with new_string — this mutates the file on disk. Prefer this over host-native edit tools: the " +
	"post-edit content is captured and indexed, keeping this file's read label current. old_string must " +
	"match the file bytes exactly once, including whitespace and line endings; zero or multiple matches " +
	"fail without touching the file. Small post-edit files come back verbatim (doubling as verification " +
	"of the applied change). Larger files come back as a compact summary plus a source label ending in " +
	"@<token>; fetch the content with calm_search source=<label exactly as returned>. The label refers " +
	"to the latest content of this file (the same label calm_read_file emits); for the file state after " +
	"one specific past edit, use calm:v1:file:edit:<path>#<n> without any @<token>. Never append #<n> " +
	"after the @<token>."

const writeFileDescription = "Write full content to a workspace file, creating it or replacing its " +
	"entire contents — this mutates the file on disk. Prefer this over host-native write tools: the new " +
	"content is captured and indexed, keeping this file's read label current. For partial changes to an " +
	"existing file prefer calm_edit_file. Small files come back verbatim (doubling as verification). " +
	"Larger files come back as a compact summary plus a source label ending in @<token>; fetch the " +
	"content with calm_search source=<label exactly as returned>. The label refers to the latest content " +
	"of this file (the same label calm_read_file emits); for the file state after one specific past " +
	"write, use calm:v1:file:edit:<path>#<n> without any @<token>. Never append #<n> after the @<token>."

const editFileSchema = `{
  "type": "object",
  "properties": {
    "path": {"type": "string", "description": "File path, workspace-relative."},
    "old_string": {"type": "string", "description": "Exact bytes to replace; must match the file exactly once, including whitespace and line endings."},
    "new_string": {"type": "string", "description": "Replacement bytes; may be empty to delete the matched text."}
  },
  "required": ["path", "old_string", "new_string"],
  "additionalProperties": false
}`

const writeFileSchema = `{
  "type": "object",
  "properties": {
    "path": {"type": "string", "description": "File path, workspace-relative; created if absent."},
    "content": {"type": "string", "description": "Full file content, written verbatim."}
  },
  "required": ["path", "content"],
  "additionalProperties": false
}`

type editFileArgs struct {
	Path      string `json:"path"`
	OldString string `json:"old_string"`
	NewString string `json:"new_string"`
}

type writeFileArgs struct {
	Path    string `json:"path"`
	Content string `json:"content"`
}

func (s *Server) newEditFileTool() Tool {
	return Tool{
		Name:        toolNameEditFile,
		Description: editFileDescription,
		InputSchema: json.RawMessage(editFileSchema),
		Handler:     s.editFile,
	}
}

func (s *Server) newWriteFileTool() Tool {
	return Tool{
		Name:        toolNameWriteFile,
		Description: writeFileDescription,
		InputSchema: json.RawMessage(writeFileSchema),
		Handler:     s.writeFile,
	}
}

func (s *Server) editFile(ctx context.Context, args json.RawMessage) (ToolResult, error) {
	var a editFileArgs
	if uerr := json.Unmarshal(args, &a); uerr != nil {
		return ToolResult{}, &ArgError{Detail: uerr.Error()}
	}
	if strings.TrimSpace(a.Path) == "" {
		return ToolResult{}, &ArgError{Detail: "path is required"}
	}
	if a.OldString == "" {
		return ToolResult{}, &ArgError{Detail: "old_string is required; use calm_write_file for full rewrites"}
	}
	if a.OldString == a.NewString {
		return ToolResult{}, &ArgError{Detail: "old_string and new_string are identical — nothing to change"}
	}

	abs := s.resolveWorkspacePath(a.Path)
	fi, serr := os.Stat(abs)
	if serr != nil {
		return TextResult("edit failed: "+serr.Error(), true), nil
	}
	if fi.IsDir() {
		return TextResult("edit failed: "+a.Path+" is a directory", true), nil
	}

	old, rerr := readFull(abs)
	if rerr != nil {
		return TextResult("edit failed: "+rerr.Error(), true), nil
	}

	n := strings.Count(old, a.OldString)
	if n != 1 {
		return TextResult(fmt.Sprintf(
			"edit failed: old_string matches %d times; it must match exactly once — add surrounding context to make it unique", n), true), nil
	}
	newContent := strings.Replace(old, a.OldString, a.NewString, 1)
	if len(newContent) > exec.MaxOutputBytes {
		return TextResult(oversizeMsg("edit"), true), nil
	}

	if werr := atomicWriteFile(abs, newContent, fi.Mode().Perm()); werr != nil {
		return TextResult("edit failed: "+werr.Error(), true), nil
	}

	r := exec.Result{Stdout: newContent}
	return s.capturePipeline(ctx, captureSpec{
		ingest:  newContent,
		visible: newContent,
		res:     r,
		plan: func() (extract.Plan, error) {
			inv := extract.Invocation{Seq: s.seq.Add(1), Cwd: s.workspaceRoot, WorkspaceRoot: s.workspaceRoot}
			return extract.PlanFileEdit(inv, execResultOf(r), a.Path, old, newContent), nil
		},
	})
}

func (s *Server) writeFile(ctx context.Context, args json.RawMessage) (ToolResult, error) {
	var a writeFileArgs
	if uerr := json.Unmarshal(args, &a); uerr != nil {
		return ToolResult{}, &ArgError{Detail: uerr.Error()}
	}
	if strings.TrimSpace(a.Path) == "" {
		return ToolResult{}, &ArgError{Detail: "path is required"}
	}
	if len(a.Content) > exec.MaxOutputBytes {
		return TextResult(oversizeMsg("write"), true), nil
	}

	abs := s.resolveWorkspacePath(a.Path)
	op := extract.OperationWrite
	perm := os.FileMode(0o600)
	old := ""
	fi, serr := os.Stat(abs)
	switch {
	case os.IsNotExist(serr):
		op = extract.OperationCreate
	case serr != nil:
		return TextResult("write failed: "+serr.Error(), true), nil
	case fi.IsDir():
		return TextResult("write failed: "+a.Path+" is a directory", true), nil
	default:
		perm = fi.Mode().Perm()
		var rerr error
		if old, rerr = readFull(abs); rerr != nil {
			return TextResult("write failed: "+rerr.Error(), true), nil
		}
	}

	if werr := atomicWriteFile(abs, a.Content, perm); werr != nil {
		return TextResult("write failed: "+werr.Error(), true), nil
	}

	r := exec.Result{Stdout: a.Content}
	return s.capturePipeline(ctx, captureSpec{
		ingest:  a.Content,
		visible: a.Content,
		res:     r,
		plan: func() (extract.Plan, error) {
			inv := extract.Invocation{Seq: s.seq.Add(1), Cwd: s.workspaceRoot, WorkspaceRoot: s.workspaceRoot}
			return extract.PlanFileWrite(inv, execResultOf(r), a.Path, op, old, a.Content), nil
		},
	})
}

// readFull reads the whole file with the oversize refusal: the capture cap is
// the edit cap — mutating a beyond-cap file would ingest truncated content
// under the shared file:read latest identity and poison read-after-edit.
func readFull(path string) (string, error) {
	//nolint:gosec // DL02: local file access is an adapter capability; the workspace boundary is labeling-only, not access control (LABELING.md §4)
	b, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	if len(b) > exec.MaxOutputBytes {
		return "", fmt.Errorf("file exceeds the %dKiB capture limit; use calm_run_command for out-of-band changes", exec.MaxOutputBytes/1024)
	}
	return string(b), nil
}

func oversizeMsg(op string) string {
	return fmt.Sprintf("%s failed: content exceeds the %dKiB capture limit; use calm_run_command for out-of-band changes", op, exec.MaxOutputBytes/1024)
}

// atomicWriteFile writes via a temp file in the target's own directory plus
// rename, so a crash never leaves a half-written target and the rename works
// over an existing file on Windows (MoveFileEx semantics). Content lands
// verbatim — no EOL translation.
func atomicWriteFile(path, content string, perm os.FileMode) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".calm-write-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	cleanup := func(e error) error {
		_ = tmp.Close()
		_ = os.Remove(tmpName)
		return e
	}
	if _, err := tmp.WriteString(content); err != nil {
		return cleanup(err)
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpName)
		return err
	}
	// CreateTemp mints 0600; apply the preserved (or default) target mode.
	//nolint:gosec // G302: the mode is the target's pre-existing permission, preserved deliberately
	if err := os.Chmod(tmpName, perm); err != nil {
		_ = os.Remove(tmpName)
		return err
	}
	if err := os.Rename(tmpName, path); err != nil {
		_ = os.Remove(tmpName)
		return err
	}
	return nil
}
