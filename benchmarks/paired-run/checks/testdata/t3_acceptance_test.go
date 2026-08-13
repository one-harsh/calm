// Copyright 2026 The CALM Authors
// SPDX-License-Identifier: Apache-2.0

package mcp_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/one-harsh/calm/internal/adapter/calm"
)

// Oracle for the replace_all option on calm_edit_file. It drives the real tool
// in-process through the MCP JSON-RPC seam (shared harness) against a mock CALM
// client, and inspects the advertised tool contract via tools/list. The task
// pins: (a) single match, flag off -> exact-once unchanged (post-edit content
// echoed verbatim, no count); (b) single match, flag on -> replaced and the
// response reports count 1; (c) many matches, flag on -> every occurrence
// replaced, the count reported, and the capture/label contract intact
// (history-then-latest dual write + file_touched edit event); (d) many matches,
// flag off -> count-bearing error, file untouched; (e) zero matches -> error.
// Plus the LLM-facing contract: the input schema exposes replace_all and the
// description mentions it. On the unfixed tree the flag is absent from the
// schema and silently ignored at runtime, so the flag-on cases and the contract
// check fail — the oracle rejects the unfixed tree.

// (a) replace_all=false on a single match keeps exact-once semantics: the file
// is edited and the mutation succeeds under the tool's standing response
// contract (a confirmation carrying the successor basis; content not echoed).
func TestReplaceAllOracle_SingleMatchFlagOff(t *testing.T) {
	m := calm.NewMockClient(t)
	inspectSession(t, m)
	sourcesCapture(m, 3)

	ws := t.TempDir()
	writeWorkspaceFileMCP(t, ws, "f.txt", "alpha NEEDLE omega\n")
	h := newWorkspaceHarness(t, m, ws)
	initSession(t, h, "claude-code")

	basis := readBasis(t, h, 2, m, "f.txt")
	eventsDone, _ := eventCapture(m)
	res := callTool(t, h, 3, "calm_edit_file", map[string]any{
		"path": "f.txt", "basis": basis, "old_string": "NEEDLE", "new_string": "MET", "replace_all": false,
	})
	if res.IsError {
		t.Fatalf("flag-off single edit errored: %+v", res)
	}
	disk, _ := os.ReadFile(filepath.Join(ws, "f.txt"))
	if string(disk) != "alpha MET omega\n" {
		t.Errorf("disk = %q; want the single replacement applied", disk)
	}
	awaitSignal(t, eventsDone)
}

// (b) replace_all=true on a single match still replaces it and reports a count
// of 1 in the response.
func TestReplaceAllOracle_SingleMatchFlagOnReportsCountOne(t *testing.T) {
	m := calm.NewMockClient(t)
	inspectSession(t, m)
	sourcesCapture(m, 3)

	ws := t.TempDir()
	writeWorkspaceFileMCP(t, ws, "f.txt", "alpha NEEDLE omega\n")
	h := newWorkspaceHarness(t, m, ws)
	initSession(t, h, "claude-code")

	basis := readBasis(t, h, 2, m, "f.txt")
	eventsDone, _ := eventCapture(m)
	res := callTool(t, h, 3, "calm_edit_file", map[string]any{
		"path": "f.txt", "basis": basis, "old_string": "NEEDLE", "new_string": "MET", "replace_all": true,
	})
	if res.IsError {
		t.Fatalf("flag-on single edit errored: %+v", res)
	}
	if got := resultText(t, res); !regexp.MustCompile(`\b1\b`).MatchString(got) {
		t.Errorf("flag-on visible = %q; want the replacement count (1) reported", got)
	}
	disk, _ := os.ReadFile(filepath.Join(ws, "f.txt"))
	if string(disk) != "alpha MET omega\n" {
		t.Errorf("disk = %q; want the single replacement applied", disk)
	}
	awaitSignal(t, eventsDone)
}

// (c) replace_all=true on multiple matches replaces every occurrence, reports
// the count, and preserves the capture/label contract: history-then-latest dual
// write under the same source labels and a file_touched edit event.
func TestReplaceAllOracle_MultiMatchReplacesAllWithCountAndCapture(t *testing.T) {
	m := calm.NewMockClient(t)
	inspectSession(t, m)
	mu, sources := sourcesCapture(m, 3)

	ws := t.TempDir()
	writeWorkspaceFileMCP(t, ws, "f.txt", "a NEEDLE b NEEDLE c NEEDLE d\n")
	h := newWorkspaceHarness(t, m, ws)
	initSession(t, h, "claude-code")

	basis := readBasis(t, h, 2, m, "f.txt")
	eventsDone, events := eventCapture(m)
	res := callTool(t, h, 3, "calm_edit_file", map[string]any{
		"path": "f.txt", "basis": basis, "old_string": "NEEDLE", "new_string": "MET", "replace_all": true,
	})
	if res.IsError {
		t.Fatalf("multi-match replace_all errored: %+v", res)
	}
	if got := resultText(t, res); !regexp.MustCompile(`\b3\b`).MatchString(got) {
		t.Errorf("visible = %q; want the replacement count (3) reported", got)
	}
	disk, _ := os.ReadFile(filepath.Join(ws, "f.txt"))
	if string(disk) != "a MET b MET c MET d\n" {
		t.Errorf("disk = %q; want every occurrence replaced", disk)
	}
	if strings.Contains(string(disk), "NEEDLE") {
		t.Errorf("disk = %q; still carries an unreplaced occurrence", disk)
	}
	awaitSignal(t, eventsDone)

	mu.Lock()
	want := []string{"calm:v1:file:read:f.txt", "calm:v1:file:edit:f.txt#2", "calm:v1:file:read:f.txt"}
	if len(*sources) != 3 || (*sources)[0] != want[0] || (*sources)[1] != want[1] || (*sources)[2] != want[2] {
		t.Errorf("ingest order = %v; want basis read, then history-then-latest %v (capture/label contract)", *sources, want)
	}
	mu.Unlock()

	var ft map[string]any
	for _, e := range *events {
		if e.Type == "file_touched" {
			ft = e.Data
		}
	}
	if ft == nil {
		t.Fatalf("no file_touched event on multi-edit: %+v", *events)
	}
	if ft["operation"] != "edit" || ft["path"] != "f.txt" {
		t.Errorf("file_touched payload = %+v; want operation=edit path=f.txt", ft)
	}
}

// (d)+(e) Error paths stay as promised: without the flag, multiple matches fail
// with a count-bearing error and the file is untouched; and a zero-match edit is
// an error even with replace_all set — there is nothing to replace. Beyond the
// opening basis read, no ingest/event expectations exist, proving the failed
// edits capture nothing.
func TestReplaceAllOracle_ErrorPathsUnchanged(t *testing.T) {
	m := calm.NewMockClient(t)
	inspectSession(t, m)
	sourcesCapture(m, 1)

	ws := t.TempDir()
	const content = "a NEEDLE b NEEDLE c NEEDLE d\n"
	writeWorkspaceFileMCP(t, ws, "f.txt", content)
	h := newWorkspaceHarness(t, m, ws)
	initSession(t, h, "claude-code")

	basis := readBasis(t, h, 2, m, "f.txt")

	res := callTool(t, h, 3, "calm_edit_file", map[string]any{
		"path": "f.txt", "basis": basis, "old_string": "NEEDLE", "new_string": "MET", "replace_all": false,
	})
	if !res.IsError || !strings.Contains(resultText(t, res), "matches 3 times") {
		t.Errorf("multi-match without flag = %+v; want count-bearing failure", res)
	}

	res = callTool(t, h, 4, "calm_edit_file", map[string]any{
		"path": "f.txt", "basis": basis, "old_string": "ABSENT", "new_string": "MET", "replace_all": true,
	})
	if !res.IsError {
		t.Errorf("zero-match with flag = %+v; want a failure", res)
	}

	disk, _ := os.ReadFile(filepath.Join(ws, "f.txt"))
	if string(disk) != content {
		t.Errorf("file changed on failed edits: %q", disk)
	}
}

// The LLM-facing contract advertises the option: the input schema exposes a
// replace_all field and the tool description mentions it.
func TestReplaceAllOracle_SchemaAndDescriptionExposeFlag(t *testing.T) {
	h := newHarness(t, calm.NewMockClient(t))
	h.send(req(1, "tools/list", nil))
	r := h.recv()
	var list struct {
		Tools []struct {
			Name        string          `json:"name"`
			Description string          `json:"description"`
			InputSchema json.RawMessage `json:"inputSchema"`
		} `json:"tools"`
	}
	if err := json.Unmarshal(r.Result, &list); err != nil {
		t.Fatalf("decode tools/list: %v", err)
	}
	var found bool
	for _, tool := range list.Tools {
		if tool.Name != "calm_edit_file" {
			continue
		}
		found = true
		if !strings.Contains(string(tool.InputSchema), "replace_all") {
			t.Errorf("calm_edit_file input schema does not expose replace_all: %s", tool.InputSchema)
		}
		if !strings.Contains(tool.Description, "replace_all") {
			t.Errorf("calm_edit_file description does not mention replace_all: %q", tool.Description)
		}
	}
	if !found {
		t.Fatal("calm_edit_file absent from tools/list")
	}
}
