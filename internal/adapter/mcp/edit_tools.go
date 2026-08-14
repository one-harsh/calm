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

	logging "github.com/one-harsh/context-logging"

	"github.com/one-harsh/calm/internal/adapter/capture"
	"github.com/one-harsh/calm/internal/adapter/exec"
	"github.com/one-harsh/calm/internal/adapter/extract"
	"github.com/one-harsh/calm/internal/adapter/obs"
)

const (
	toolNameEditFile  = "calm_edit_file"
	toolNameWriteFile = "calm_write_file"
)

const editFileDescription = "Edit a workspace file by replacing old_string with new_string — this " +
	"mutates the file on disk. Prefer this over host-native edit tools: the " +
	"post-edit content is captured and indexed, keeping this file's read label current. " +
	"Requires basis: the source label ending in @<token> that your latest calm_read_file, calm_edit_file, " +
	"or calm_write_file of this same path returned. The basis asserts you know the file's current bytes " +
	"and is checked against the file on disk before anything is written, so a file changed behind your " +
	"back is never silently overwritten. old_string must match the file bytes exactly, including " +
	"whitespace and line endings; by default it must match exactly once, and zero or multiple matches " +
	"fail without touching the file. Set replace_all=true to replace every occurrence instead — the " +
	"confirmation then reports how many were replaced, and a zero-match still fails. " +
	"On success the response is a short confirmation plus basis=<new label> — the edited content is not " +
	"echoed; pass that label as the basis of your next edit or write of this file, and read the new " +
	"content with calm_search source=<that label> if you need to see it. If the basis is missing, unknown, " +
	"or no longer matches the file, nothing is written: the response recaptures the file and hands you its " +
	"fresh label plus a size summary, so you can retry from the response alone without re-reading. " +
	"For the file state after one specific past edit, use calm:v1:file:edit:<path>#<n> without any " +
	"@<token>. Never append #<n> after the @<token>. In multi-workspace sessions, set workspace=<id> to " +
	"target a non-default workspace."

const writeFileDescription = "Write full content to a workspace file, creating it or replacing its " +
	"entire contents — this mutates the file on disk. Prefer this over host-native write tools: the new " +
	"content is captured and indexed, keeping this file's read label current. For partial changes to an " +
	"existing file prefer calm_edit_file. " +
	"Creating a file that does not exist needs no basis — the absence is the assertion. Replacing a file " +
	"that does exist requires basis: the source label ending in @<token> that your latest calm_read_file, " +
	"calm_edit_file, or calm_write_file of this same path returned. An existing file is never overwritten " +
	"without a current basis, so a path you expected to be free is a rejection rather than data loss. " +
	"On success the response is a short confirmation plus basis=<new label> — the content is not echoed " +
	"back; pass that label as the basis of your next edit or write of this file, and read it with " +
	"calm_search source=<that label> if you need to see it. If the file exists and the basis is missing, " +
	"unknown, or no longer matches it, nothing is written: the response recaptures the file and hands you " +
	"its fresh label plus a size summary. A rejection's label must be read before it authorizes an " +
	"overwrite — retrieve it with calm_search source=<that label>, then retry with the same basis; a full " +
	"rewrite of content you have not seen is refused. " +
	"For the file state after one specific past write, use calm:v1:file:edit:<path>#<n> without any " +
	"@<token>. Never append #<n> after the @<token>. In multi-workspace sessions, set workspace=<id> to " +
	"target a non-default workspace."

const editFileSchema = `{
  "type": "object",
  "properties": {
    "path": {"type": "string", "description": "File path, workspace-relative."},
    "basis": {"type": "string", "description": "Source label ending in @<token> from your latest read, edit, or write capture of this path; asserts you know the file's current bytes."},
    "old_string": {"type": "string", "description": "Exact bytes to replace, including whitespace and line endings; must match the file exactly once unless replace_all is true."},
    "new_string": {"type": "string", "description": "Replacement bytes; may be empty to delete the matched text."},
    "replace_all": {"type": "boolean", "description": "Replace every occurrence of old_string instead of requiring a single match; defaults to false. The confirmation reports how many were replaced; a zero-match still fails."},
    "workspace": {"type": "string", "description": "Workspace ID to target; defaults to the primary workspace."}
  },
  "required": ["path", "basis", "old_string", "new_string"],
  "additionalProperties": false
}`

const writeFileSchema = `{
  "type": "object",
  "properties": {
    "path": {"type": "string", "description": "File path, workspace-relative; created if absent."},
    "content": {"type": "string", "description": "Full file content, written verbatim."},
    "basis": {"type": "string", "description": "Source label ending in @<token> from your latest read, edit, or write capture of this path. Required when the file already exists; omit when creating a new file."},
    "workspace": {"type": "string", "description": "Workspace ID to target; defaults to the primary workspace."}
  },
  "required": ["path", "content"],
  "additionalProperties": false
}`

type editFileArgs struct {
	Path       string `json:"path"`
	Basis      string `json:"basis"`
	OldString  string `json:"old_string"`
	NewString  string `json:"new_string"`
	ReplaceAll bool   `json:"replace_all"`
	Workspace  string `json:"workspace"`
}

type writeFileArgs struct {
	Path      string `json:"path"`
	Content   string `json:"content"`
	Basis     string `json:"basis"`
	Workspace string `json:"workspace"`
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
	s.mutations.Lock()
	defer s.mutations.Unlock()

	wb, werr := s.workspaceForPath(a.Workspace, a.Path)
	if werr != nil {
		return ToolResult{}, werr
	}
	abs := wb.resolve(a.Path)
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

	if v := s.verifyBasis(a.Basis, abs, old, "no basis was supplied"); !v.ok {
		return s.rejectMutation(ctx, "edit", wb, a.Path, abs, old, v)
	}

	n := strings.Count(old, a.OldString)
	switch {
	case n == 0:
		return TextResult(fmt.Sprintf(
			"edit failed: old_string matches %d times; it must appear in the file — check the exact bytes, including whitespace and line endings", n,
		), true), nil
	case n > 1 && !a.ReplaceAll:
		return TextResult(fmt.Sprintf(
			"edit failed: old_string matches %d times; it must match exactly once — add surrounding context to make it unique, or set replace_all=true to replace every occurrence", n,
		), true), nil
	}

	limit := 1
	detail := ""
	if a.ReplaceAll {
		limit = n
		detail = fmt.Sprintf("replaced %d %s", n, plural(n, "occurrence", "occurrences"))
	}
	newContent := strings.Replace(old, a.OldString, a.NewString, limit)
	if len(newContent) > exec.MaxOutputBytes {
		return TextResult(oversizeMsg("edit"), true), nil
	}

	if werr := atomicWriteFile(abs, newContent, fi.Mode().Perm()); werr != nil {
		return TextResult("edit failed: "+werr.Error(), true), nil
	}

	r := exec.Result{Stdout: newContent}
	return s.confirmMutation(ctx, "edited", detail, a.Path, abs, newContent, capture.Spec{
		Ingest:  newContent,
		Visible: newContent,
		Res:     r,
		Plan: func(seq int64) (extract.Plan, error) {
			return extract.PlanFileEdit(s.invocation(seq, wb, "", wb.Root), execResultOf(r), a.Path, old, newContent), nil
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

	s.mutations.Lock()
	defer s.mutations.Unlock()

	wb, werr := s.workspaceForPath(a.Workspace, a.Path)
	if werr != nil {
		return ToolResult{}, werr
	}
	abs := wb.resolve(a.Path)
	op := extract.OperationWrite
	verb := "wrote"
	perm := os.FileMode(0o600)
	old := ""
	fi, serr := os.Stat(abs)
	switch {
	case os.IsNotExist(serr):
		// A basis names a file the caller believes exists; recreating over an
		// out-of-band deletion would silently undo it. The rejection carries no
		// recapture — there is nothing to capture — but names the retry.
		if strings.TrimSpace(a.Basis) != "" {
			logging.BindSummary(ctx, obs.PresentationModeFieldSummary)
			s.log.WithContext(ctx).Debug("mutation basis rejected",
				logging.StringField(keyBasisState, basisStateDeleted))
			return TextResult(fmt.Sprintf(
				"write rejected: the file was deleted since basis %s. %s does not exist. "+
					"To create it fresh, retry without basis.",
				a.Basis, a.Path,
			), true), nil
		}
		op = extract.OperationCreate
		verb = "created"
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
		if v := s.verifyBasis(a.Basis, abs, old, "no basis was supplied and the file already exists"); !v.ok {
			return s.rejectMutation(ctx, "write", wb, a.Path, abs, old, v)
		}
		// A full overwrite has no old_string anchor proving the caller saw what it
		// replaces, so possession of a rejection-minted basis is not enough — the
		// content must have been read back through this shell first.
		if s.basis.Unread(a.Basis) {
			logging.BindSummary(ctx, obs.PresentationModeFieldSummary)
			s.log.WithContext(ctx).Debug("mutation basis rejected",
				logging.StringField(keyBasisState, basisStateUnreadWrite))
			return TextResult(fmt.Sprintf(
				"write rejected: basis %s was issued by a rejection and its content has not been read. "+
					"Read it first — %s source=%s — then retry with the same basis.",
				a.Basis, toolNameSearch, a.Basis,
			), true), nil
		}
	}

	if werr := atomicWriteFile(abs, a.Content, perm); werr != nil {
		return TextResult("write failed: "+werr.Error(), true), nil
	}

	r := exec.Result{Stdout: a.Content}
	return s.confirmMutation(ctx, verb, "", a.Path, abs, a.Content, capture.Spec{
		Ingest:  a.Content,
		Visible: a.Content,
		Res:     r,
		Plan: func(seq int64) (extract.Plan, error) {
			return extract.PlanFileWrite(s.invocation(seq, wb, "", wb.Root), execResultOf(r), a.Path, op, old, a.Content), nil
		},
	})
}

// basis states name why a supplied basis did not hold; they are a closed set so
// the operator can slice rejections without reading the agent-facing text.
const (
	basisStateMissing     = "missing"
	basisStateUnknown     = "unknown"
	basisStateStale       = "stale"
	basisStateUnreadWrite = "unread_write"
	basisStateDeleted     = "deleted"
	keyBasisState         = "basis_state"
)

type basisVerdict struct {
	ok    bool
	state string
	cause string // agent-facing clause naming what did not hold
}

func (s *Server) verifyBasis(basis, abs, current, missingCause string) basisVerdict {
	if strings.TrimSpace(basis) == "" {
		return basisVerdict{state: basisStateMissing, cause: missingCause}
	}
	known, fresh := s.basis.Verify(basis, abs, current)
	switch {
	case !known:
		return basisVerdict{state: basisStateUnknown, cause: "basis " + basis + " is not a capture this session recorded for this file"}
	case !fresh:
		return basisVerdict{state: basisStateStale, cause: "the file changed since basis " + basis}
	}
	return basisVerdict{ok: true}
}

// confirmMutation is the bare success shape: what changed, and the label that is
// the next mutation's basis. The content itself never rides back — the label
// addresses it, and echoing it would restore the view the basis requirement
// makes unnecessary.
func (s *Server) confirmMutation(ctx context.Context, verb, detail, path, abs, content string, spec capture.Spec) (ToolResult, error) {
	out := s.engine.Capture(ctx, spec)
	s.basis.Record(out.Label, abs, content)
	logging.BindSummary(ctx, obs.PresentationModeFieldSummary)

	var b strings.Builder
	fmt.Fprintf(&b, "%s %s (%d bytes)", verb, path, len(content))
	if detail != "" {
		fmt.Fprintf(&b, ", %s", detail)
	}
	b.WriteString(".\n")
	writeBasisLine(&b, out.Label, "pass this as basis for the next edit or write of this file; read the new content with")
	return s.mutationResult(TextResult(b.String(), false), out)
}

// rejectMutation is the productive half of the contract: the rejection performs
// the re-read the caller would otherwise have to make, so the response alone
// carries everything a retry needs.
func (s *Server) rejectMutation(ctx context.Context, verb string, wb WorkspaceBinding, path, abs, current string, v basisVerdict) (ToolResult, error) {
	s.log.WithContext(ctx).Debug("mutation basis rejected",
		logging.StringField(keyBasisState, v.state))

	r := exec.Result{Stdout: current}
	out := s.engine.Capture(ctx, capture.Spec{
		Ingest:  current,
		Visible: current,
		Res:     r,
		Plan: func(seq int64) (extract.Plan, error) {
			return extract.PlanFileRead(s.invocation(seq, wb, "", wb.Root), execResultOf(r), path), nil
		},
	})
	s.basis.RecordUnread(out.Label, abs, current)
	logging.BindSummary(ctx, obs.PresentationModeFieldSummary)

	var b strings.Builder
	fmt.Fprintf(&b, "%s rejected: %s. %s is unchanged.\n", verb, v.cause, path)
	fmt.Fprintf(&b, "Current state: %d bytes, %d lines.\n", len(current), lineCount(current))
	// The named next action must be the one that succeeds: an edit retry is
	// anchored by its old text, but an overwrite unlocks only after the read.
	if verb == "write" {
		writeBasisLine(&b, out.Label, "read the current content with")
		if out.Label != "" {
			b.WriteString(", then retry with this basis — an unread rejection basis does not authorize an overwrite")
		}
	} else {
		writeBasisLine(&b, out.Label, "retry with this basis — the edit lands only where your old text still matches; to see what changed first, read it with")
	}
	return s.mutationResult(TextResult(b.String(), true), out)
}

// writeBasisLine names the token the next action needs, or says plainly that
// none exists — a degraded capture mints no label, and the agent must learn that
// from the response rather than from a later rejection.
func writeBasisLine(b *strings.Builder, label, guidance string) {
	if label == "" {
		b.WriteString("Capture is degraded, so no basis label was minted; " +
			toolNameEditFile + " and " + toolNameWriteFile + " on this file are rejected until capture recovers — " +
			"use the host's native edit tools in the meantime.")
		return
	}
	fmt.Fprintf(b, "basis=%s — %s %s source=%s", label, guidance, toolNameSearch, label)
}

// mutationResult carries the engine's degradation verdict out alongside the
// composed text so the shell layers its canonical phrasing; the local mutation
// already happened either way (never-worse).
func (s *Server) mutationResult(res ToolResult, out capture.Outcome) (ToolResult, error) {
	if out.Reason == "" {
		return res, nil
	}
	return res, &DegradedSignal{Reason: out.Reason, Detail: out.Detail}
}

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}

func lineCount(s string) int {
	if s == "" {
		return 0
	}
	n := strings.Count(s, "\n")
	if !strings.HasSuffix(s, "\n") {
		n++
	}
	return n
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
