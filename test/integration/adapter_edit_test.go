// Copyright 2026 The CALM Authors
// SPDX-License-Identifier: Apache-2.0

package integration

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// parseBasis reads the successor basis a mutation response hands back — the
// label the next mutation of that path must name, and the only address of the
// content the response deliberately did not echo.
func parseBasis(t *testing.T, text string) string {
	t.Helper()
	const key = "basis="
	idx := strings.Index(text, key)
	if idx < 0 {
		t.Fatalf("mutation response carries no successor basis:\n%s", text)
	}
	rest := text[idx+len(key):]
	if cut := strings.IndexAny(rest, " \n"); cut >= 0 {
		return rest[:cut]
	}
	return rest
}

// A chain of edits pays one read at first touch and none after: each response's
// label is the next edit's basis, the final bytes land on disk and in CALM, and
// every edit's own snapshot stays individually retrievable.
func TestAdapterEdit_BasisChainedEditsLandOnDiskAndInCALM(t *testing.T) {
	workspace := t.TempDir()
	base := "package main\n\n// marker: zphloxstate0\n" + inlinePad
	writeWorkspaceFile(t, workspace, "foo.go", base)

	_, token, d := newAdapterLoop(t, workspace)

	first := d.callTool("calm_read_file", map[string]any{"path": "foo.go"})
	if first.IsError {
		t.Fatalf("first-touch read errored: %+v", first)
	}
	basis := parseSearchSourceFused(t, first.Content[0].Text)

	edit := func(i int, from string) string {
		t.Helper()
		res := d.callTool("calm_edit_file", map[string]any{
			"path":       "foo.go",
			"basis":      from,
			"old_string": fmt.Sprintf("zphloxstate%d", i-1),
			"new_string": fmt.Sprintf("zphloxstate%d", i),
		})
		if res.IsError {
			t.Fatalf("edit %d errored: %+v", i, res)
		}
		if strings.Contains(res.Content[0].Text, "zphloxstate") {
			t.Fatalf("edit %d echoed file content back:\n%s", i, res.Content[0].Text)
		}
		return parseBasis(t, res.Content[0].Text)
	}

	// First edit alone, so the single file_touched event read back is
	// deterministically edit 1's (event retrieval order is not emission order).
	basis = edit(1, basis)
	_, ft := eventData(t, token, "file_touched")
	seq, ok := ft["invocation_id"].(float64)
	if !ok {
		t.Fatalf("file_touched missing invocation_id: %+v", ft)
	}
	if ft["operation"] != "edit" {
		t.Errorf("operation = %v; want edit", ft["operation"])
	}
	if diff, _ := ft["diff"].(string); !strings.Contains(diff, "+// marker: zphloxstate1") {
		t.Errorf("diff = %q; want the first edit's addition", diff)
	}

	basis = edit(2, basis)
	basis = edit(3, basis)

	disk, err := os.ReadFile(filepath.Join(workspace, "foo.go"))
	if err != nil {
		t.Fatalf("read back foo.go: %v", err)
	}
	if !strings.Contains(string(disk), "// marker: zphloxstate3") {
		t.Fatalf("disk did not receive the final edit: %q", disk)
	}

	// The chain's last basis addresses the current content without a re-read.
	sr := d.search([]string{"zphloxstate3"}, basis)
	if sr.IsError || !strings.Contains(sr.Content[0].Text, "zphloxstate3") {
		t.Fatalf("final basis did not retrieve the final content: %+v", sr)
	}

	// The read tool converges on the same latest identity the chain wrote to.
	res := d.callTool("calm_read_file", map[string]any{"path": "foo.go"})
	if res.IsError {
		t.Fatalf("read after edits errored: %+v", res)
	}
	if latest := parseSearchSource(t, res.Content[0].Text); latest != "calm:v1:file:read:foo.go" {
		t.Fatalf("latest label = %q", latest)
	}

	// The first edit's history snapshot via base-only retrieval (the history
	// token is never advertised; base-only forwards without staleness checking
	// by design).
	historyLabel := fmt.Sprintf("calm:v1:file:edit:foo.go#%d", int64(seq))
	hr := d.search([]string{"zphloxstate1"}, historyLabel)
	if hr.IsError || !strings.Contains(hr.Content[0].Text, "zphloxstate1") {
		t.Fatalf("history snapshot %q missing post-edit-1 marker: %+v", historyLabel, hr)
	}
	if strings.Contains(hr.Content[0].Text, "zphloxstate3") {
		t.Fatalf("history snapshot %q leaked the final state", historyLabel)
	}
}

// A file changed behind the agent's back stops the chain rather than losing the
// change: the edit is refused, the rejection is itself the re-read, and the
// label it returns is enough to continue.
func TestAdapterEdit_ExternalChangeMidChainRejectsThenRecovers(t *testing.T) {
	workspace := t.TempDir()
	writeWorkspaceFile(t, workspace, "bar.go", "// marker: zphloxdrift0\n"+inlinePad)

	_, _, d := newAdapterLoop(t, workspace)

	read := d.callTool("calm_read_file", map[string]any{"path": "bar.go"})
	if read.IsError {
		t.Fatalf("first-touch read errored: %+v", read)
	}
	basis := parseSearchSourceFused(t, read.Content[0].Text)

	drifted := "// marker: zphloxdrift0\n// zphloxoutofband\n" + inlinePad
	writeWorkspaceFile(t, workspace, "bar.go", drifted)

	rejected := d.callTool("calm_edit_file", map[string]any{
		"path": "bar.go", "basis": basis,
		"old_string": "zphloxdrift0", "new_string": "zphloxdrift1",
	})
	if !rejected.IsError {
		t.Fatalf("an edit onto changed bytes must be refused: %+v", rejected)
	}
	if !strings.Contains(rejected.Content[0].Text, "the file changed since basis "+basis) {
		t.Fatalf("rejection must name the outgrown basis:\n%s", rejected.Content[0].Text)
	}
	if disk, _ := os.ReadFile(filepath.Join(workspace, "bar.go")); string(disk) != drifted {
		t.Fatalf("a refused edit must leave the out-of-band change intact: %q", disk)
	}

	applied := d.callTool("calm_edit_file", map[string]any{
		"path": "bar.go", "basis": parseBasis(t, rejected.Content[0].Text),
		"old_string": "zphloxdrift0", "new_string": "zphloxdrift1",
	})
	if applied.IsError {
		t.Fatalf("retry with the rejection's basis errored: %+v", applied)
	}
	disk, err := os.ReadFile(filepath.Join(workspace, "bar.go"))
	if err != nil {
		t.Fatalf("read back bar.go: %v", err)
	}
	if !strings.Contains(string(disk), "zphloxdrift1") || !strings.Contains(string(disk), "zphloxoutofband") {
		t.Fatalf("recovered edit must sit on top of the out-of-band change: %q", disk)
	}

	sr := d.search([]string{"zphloxdrift1"}, parseBasis(t, applied.Content[0].Text))
	if sr.IsError || !strings.Contains(sr.Content[0].Text, "zphloxdrift1") {
		t.Fatalf("recovered content not retrievable through the new basis: %+v", sr)
	}
}

// write_file creates a new file with no basis to name, and the create's own
// label is what governs — and retrieves — the file from then on.
func TestAdapterEdit_WriteFileCreateLoop(t *testing.T) {
	workspace := t.TempDir()
	const marker = "zphloxcreated"

	_, token, d := newAdapterLoop(t, workspace)

	res := d.callTool("calm_write_file", map[string]any{
		"path":    "fresh.md",
		"content": "# " + marker + "\n" + inlinePad,
	})
	if res.IsError {
		t.Fatalf("write errored: %+v", res)
	}
	if strings.Contains(res.Content[0].Text, marker) {
		t.Fatalf("create echoed the written content back:\n%s", res.Content[0].Text)
	}
	fused := parseBasis(t, res.Content[0].Text)
	if !strings.HasPrefix(fused, "calm:v1:file:read:fresh.md@") {
		t.Fatalf("fused label = %q; want the read identity", fused)
	}
	sr := d.search([]string{marker}, fused)
	if sr.IsError || !strings.Contains(sr.Content[0].Text, marker) {
		t.Fatalf("created content not retrievable: %+v", sr)
	}

	_, ft := eventData(t, token, "file_touched")
	if ft["operation"] != "create" {
		t.Errorf("operation = %v; want create", ft["operation"])
	}
	if ft["path"] != "fresh.md" {
		t.Errorf("path = %v; want fresh.md", ft["path"])
	}
}
