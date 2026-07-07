// Copyright 2026 The CALM Authors
// SPDX-License-Identifier: Apache-2.0

package integration

import (
	"fmt"
	"strings"
	"testing"
)

// The AI-06 verifier loop: three successive edits through the adapter — the
// read tool returns the final state, each edit's history snapshot stays
// individually retrievable via its base-only history label, and file_touched
// events land in CALM with the right operation and diff.
func TestAdapterEdit_ThreeEditLoopWithHistory(t *testing.T) {
	workspace := t.TempDir()
	base := "package main\n\n// marker: zphloxstate0\n" + inlinePad
	writeWorkspaceFile(t, workspace, "foo.go", base)

	_, token, d := newAdapterLoop(t, workspace)

	edit := func(i int) {
		t.Helper()
		res := d.callTool("calm_edit_file", map[string]any{
			"path":       "foo.go",
			"old_string": fmt.Sprintf("zphloxstate%d", i-1),
			"new_string": fmt.Sprintf("zphloxstate%d", i),
		})
		if res.IsError {
			t.Fatalf("edit %d errored: %+v", i, res)
		}
	}

	// First edit alone, so the single file_touched event read back is
	// deterministically edit 1's (event retrieval order is not emission order).
	edit(1)
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

	edit(2)
	edit(3)

	// Latest state via the read tool.
	res := d.callTool("calm_read_file", map[string]any{"path": "foo.go"})
	if res.IsError {
		t.Fatalf("read after edits errored: %+v", res)
	}
	// Above the inline threshold the read returns a summary; verify content
	// via CALM search on the latest label instead.
	latest := parseSearchSource(t, res.Content[0].Text)
	if latest != "calm:v1:file:read:foo.go" {
		t.Fatalf("latest label = %q", latest)
	}
	sr := d.search([]string{"zphloxstate3"}, parseSearchSourceFused(t, res.Content[0].Text))
	if sr.IsError || !strings.Contains(sr.Content[0].Text, "zphloxstate3") {
		t.Fatalf("latest content missing final marker: %+v", sr)
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

// write_file creates a new file: operation=create in the event and the fused
// latest label from the response retrieves the content.
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
	fused := parseSearchSourceFused(t, res.Content[0].Text)
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
