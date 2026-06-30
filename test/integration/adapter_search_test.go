// Copyright 2026 The CALM Authors
// SPDX-License-Identifier: Apache-2.0

package integration

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/one-harsh/calm/internal/adapter/calm"
	"github.com/one-harsh/calm/internal/adapter/mcp"
	"github.com/one-harsh/calm/internal/adapter/obs"
)

func (d *mcpDriver) search(queries []string, source string) mcp.ToolResult {
	d.t.Helper()
	args := map[string]any{"queries": queries}
	if source != "" {
		args["source"] = source
	}
	r := d.call("tools/call", map[string]any{"name": "calm_search", "arguments": args})
	if r.Error != nil {
		d.t.Fatalf("tools/call protocol error: %+v", r.Error)
	}
	var res mcp.ToolResult
	if err := json.Unmarshal(r.Result, &res); err != nil {
		d.t.Fatalf("decode tool result: %v", err)
	}
	if len(res.Content) == 0 {
		d.t.Fatalf("search result had no content: %+v", res)
	}
	return res
}

// TestAdapterSearch_RunCommandThenSearchLoop closes the agent-facing loop through both MCP
// tools: capture output via calm_run_command, then retrieve it via calm_search using the
// source label the first tool advertised.
func TestAdapterSearch_RunCommandThenSearchLoop(t *testing.T) {
	workspace := t.TempDir()
	const marker = "zphloxmarker"
	writeWorkspaceFile(t, workspace, "note.txt", marker+" lives in this file\n")

	_, _, d := newAdapterLoop(t, workspace)

	rc := d.runCommand("cat note.txt")
	if rc.IsError {
		t.Fatalf("run_command errored: %+v", rc)
	}
	source := parseSearchSource(t, rc.Content[0].Text)

	res := d.search([]string{marker}, source)
	if res.IsError {
		t.Fatalf("search errored: %+v", res)
	}
	if !strings.Contains(res.Content[0].Text, marker) {
		t.Fatalf("search did not return the captured marker via source %q; got:\n%s", source, res.Content[0].Text)
	}
}

// TestAdapterSearch_SessionWideFindsCapturedOutput searches without a source filter and
// still retrieves the captured output from the session.
func TestAdapterSearch_SessionWideFindsCapturedOutput(t *testing.T) {
	workspace := t.TempDir()
	const marker = "zphloxwide"
	writeWorkspaceFile(t, workspace, "note.txt", marker+" appears here\n")

	_, _, d := newAdapterLoop(t, workspace)

	if rc := d.runCommand("cat note.txt"); rc.IsError {
		t.Fatalf("run_command errored: %+v", rc)
	}
	res := d.search([]string{marker}, "")
	if res.IsError {
		t.Fatalf("search errored: %+v", res)
	}
	if !strings.Contains(res.Content[0].Text, marker) {
		t.Fatalf("session-wide search did not find the marker; got:\n%s", res.Content[0].Text)
	}
}

// TestAdapterSearch_CalmDown_UnreachablePhrasing proves search reports the
// canonical calm_unreachable degradation phrasing (not empty results, not a
// bare error string) when CALM is unreachable — search is the operation, so
// there is no raw fallback.
func TestAdapterSearch_CalmDown_UnreachablePhrasing(t *testing.T) {
	inner, err := calm.NewGenapiClient("http://127.0.0.1:1", "", "wi39-down", nil)
	if err != nil {
		t.Fatalf("NewGenapiClient: %v", err)
	}
	d := newMCPDriver(t, inner, t.TempDir())
	if r := d.call("initialize", map[string]any{}); r.Error != nil {
		t.Fatalf("initialize must still succeed when CALM is down: %+v", r.Error)
	}

	res := d.search([]string{"zphlox"}, "")
	if !res.IsError {
		t.Fatalf("CALM-down search must be an error result: %+v", res)
	}
	want := obs.DegradedPhrase(obs.DegradedReasonCalmUnreachable)
	if res.Content[0].Text != want {
		t.Errorf("text = %q; want canonical phrasing %q", res.Content[0].Text, want)
	}
}
