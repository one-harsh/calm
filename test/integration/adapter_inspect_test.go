// Copyright 2026 The CALM Authors
// SPDX-License-Identifier: Apache-2.0

package integration

import (
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
	"testing"

	"github.com/one-harsh/calm/internal/adapter/mcp"
)

func (d *mcpDriver) callTool(name string, args map[string]any) mcp.ToolResult {
	d.t.Helper()
	r := d.call("tools/call", map[string]any{"name": name, "arguments": args})
	if r.Error != nil {
		d.t.Fatalf("tools/call protocol error: %+v", r.Error)
	}
	var res mcp.ToolResult
	if err := json.Unmarshal(r.Result, &res); err != nil {
		d.t.Fatalf("decode tool result: %v", err)
	}
	if len(res.Content) == 0 {
		d.t.Fatalf("tool result had no content: %+v", res)
	}
	return res
}

// Each structured inspection tool derives its typed label and the captured
// content round-trips through the fused recall label — the AI-05 verifier
// loop, against real CALM.
func TestAdapterInspect_ReadFileLoop(t *testing.T) {
	workspace := t.TempDir()
	const marker = "zphloxreadfile"
	writeWorkspaceFile(t, workspace, "note.txt", marker+" structured read\n"+inlinePad)

	_, _, d := newAdapterLoop(t, workspace)

	res := d.callTool("calm_read_file", map[string]any{"path": "note.txt"})
	if res.IsError {
		t.Fatalf("read_file errored: %+v", res)
	}
	if base := parseSearchSource(t, res.Content[0].Text); base != "calm:v1:file:read:note.txt" {
		t.Fatalf("source = %q; want calm:v1:file:read:note.txt", base)
	}
	fused := parseSearchSourceFused(t, res.Content[0].Text)
	sr := d.search([]string{marker}, fused)
	if sr.IsError || !strings.Contains(sr.Content[0].Text, marker) {
		t.Fatalf("fused-label retrieval failed: %+v", sr)
	}
}

// Capture-full-present-range end to end: the visible text is the requested
// slice, but the whole file is captured — content OUTSIDE the range is
// retrievable from CALM.
func TestAdapterInspect_ReadFileRange_FullCaptureSearchable(t *testing.T) {
	workspace := t.TempDir()
	const marker = "zphloxbeyondrange"
	content := "line one\nline two\n" + marker + " lives on line three\n"
	writeWorkspaceFile(t, workspace, "ranged.txt", content)

	_, _, d := newAdapterLoop(t, workspace)

	res := d.callTool("calm_read_file", map[string]any{"path": "ranged.txt", "start_line": 1, "end_line": 2})
	if res.IsError {
		t.Fatalf("ranged read errored: %+v", res)
	}
	if got := res.Content[0].Text; strings.Contains(got, marker) {
		t.Fatalf("visible text leaked content beyond the range:\n%s", got)
	}
	sr := d.search([]string{marker}, "")
	if sr.IsError || !strings.Contains(sr.Content[0].Text, marker) {
		t.Fatalf("full-file capture not searchable beyond the range: %+v", sr)
	}
}

func TestAdapterInspect_ListDirLoop(t *testing.T) {
	workspace := t.TempDir()
	const marker = "zphloxlisted"
	sub := workspace + "/src"
	writeWorkspaceFile(t, workspace, "top.txt", "x\n")
	if err := exec.Command("mkdir", "-p", sub).Run(); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	for i := range 12 {
		writeWorkspaceFile(t, sub, fmt.Sprintf("%s-%02d-%s.txt", marker, i, strings.Repeat("n", 40)), "x\n")
	}

	_, _, d := newAdapterLoop(t, workspace)

	res := d.callTool("calm_list_dir", map[string]any{"path": "src"})
	if res.IsError {
		t.Fatalf("list_dir errored: %+v", res)
	}
	if base := parseSearchSource(t, res.Content[0].Text); base != "calm:v1:file:list:src" {
		t.Fatalf("source = %q; want calm:v1:file:list:src", base)
	}
	fused := parseSearchSourceFused(t, res.Content[0].Text)
	sr := d.search([]string{marker}, fused)
	if sr.IsError || !strings.Contains(sr.Content[0].Text, marker) {
		t.Fatalf("fused-label retrieval failed: %+v", sr)
	}
}

func TestAdapterInspect_GrepLoop(t *testing.T) {
	workspace := t.TempDir()
	const marker = "zphloxgrepped"
	var b strings.Builder
	for i := range 20 {
		fmt.Fprintf(&b, "line %02d %s padding padding padding padding\n", i, marker)
	}
	writeWorkspaceFile(t, workspace, "hits.txt", b.String())

	_, _, d := newAdapterLoop(t, workspace)

	res := d.callTool("calm_grep", map[string]any{"pattern": marker})
	if res.IsError {
		t.Fatalf("grep errored: %+v", res)
	}
	if base := parseSearchSource(t, res.Content[0].Text); base != "calm:v1:search:grep:"+marker {
		t.Fatalf("source = %q; want calm:v1:search:grep:%s", base, marker)
	}
	fused := parseSearchSourceFused(t, res.Content[0].Text)
	sr := d.search([]string{"hits.txt"}, fused)
	if sr.IsError || !strings.Contains(sr.Content[0].Text, "hits.txt") {
		t.Fatalf("fused-label retrieval failed: %+v", sr)
	}
}

func TestAdapterInspect_GitStatusLoop(t *testing.T) {
	dir := gitRepo(t)
	const marker = "zphloxgitstat"
	for i := range 8 {
		writeWorkspaceFile(t, dir, fmt.Sprintf("%s-%02d-%s.txt", marker, i, strings.Repeat("p", 50)), "pad\n")
	}

	_, _, d := newAdapterLoop(t, dir)

	res := d.callTool("calm_git_status", map[string]any{})
	if res.IsError {
		t.Fatalf("git_status errored: %+v", res)
	}
	if base := parseSearchSource(t, res.Content[0].Text); base != "calm:v1:vcs:git:status" {
		t.Fatalf("source = %q; want calm:v1:vcs:git:status", base)
	}
	fused := parseSearchSourceFused(t, res.Content[0].Text)
	sr := d.search([]string{marker}, fused)
	if sr.IsError || !strings.Contains(sr.Content[0].Text, marker) {
		t.Fatalf("fused-label retrieval failed: %+v", sr)
	}
}

func TestAdapterInspect_GitDiffLoop(t *testing.T) {
	dir := gitRepo(t)
	const marker = "zphloxdiffed"
	gitCmd := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	gitCmd("add", ".")
	gitCmd("-c", "user.email=t@t", "-c", "user.name=t", "commit", "-m", "base")
	writeWorkspaceFile(t, dir, "zphloxtracked.txt", marker+" modified\n"+inlinePad)

	_, _, d := newAdapterLoop(t, dir)

	res := d.callTool("calm_git_diff", map[string]any{})
	if res.IsError {
		t.Fatalf("git_diff errored: %+v", res)
	}
	if base := parseSearchSource(t, res.Content[0].Text); base != "calm:v1:vcs:git:diff:HEAD" {
		t.Fatalf("source = %q; want calm:v1:vcs:git:diff:HEAD", base)
	}
	fused := parseSearchSourceFused(t, res.Content[0].Text)
	sr := d.search([]string{marker}, fused)
	if sr.IsError || !strings.Contains(sr.Content[0].Text, marker) {
		t.Fatalf("fused-label retrieval failed: %+v", sr)
	}
}
