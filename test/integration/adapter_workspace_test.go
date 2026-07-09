// Copyright 2026 The CALM Authors
// SPDX-License-Identifier: Apache-2.0

package integration

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The workspace-discovery verifier: the adapter launches in one repo, touches
// a sibling repo by absolute path mid-session — the sibling is discovered and
// its captures carry the WorkspaceID segment while primary captures stay
// bare, and each capture round-trips independently through its fused label.
func TestAdapterWorkspace_DiscoveryDisambiguates(t *testing.T) {
	base := t.TempDir()
	repoa := gitRepoIT(t, base, "repoa")
	repob := gitRepoIT(t, base, "repob")
	writeWorkspaceFile(t, repoa, "foo.go", "zphloxalphaws in repo a\n"+inlinePad)
	writeWorkspaceFile(t, repob, "foo.go", "zphloxbetaws in repo b\n"+inlinePad)

	_, _, d := newAdapterLoop(t, repoa)

	resA := d.callTool("calm_read_file", map[string]any{"path": "foo.go"})
	if resA.IsError {
		t.Fatalf("primary read errored: %+v", resA)
	}
	baseA := parseSearchSource(t, resA.Content[0].Text)
	if baseA != "calm:v1:file:read:foo.go" {
		t.Fatalf("primary label = %q; want bare (no workspace segment)", baseA)
	}

	resB := d.callTool("calm_read_file", map[string]any{"path": filepath.Join(repob, "foo.go")})
	if resB.IsError {
		t.Fatalf("repob read errored: %+v", resB)
	}
	baseB := parseSearchSource(t, resB.Content[0].Text)
	if baseB != "calm:v1:file:read:repob:foo.go" {
		t.Fatalf("discovered label = %q; want the repob segment", baseB)
	}

	// Each capture is independently retrievable via its fused label.
	srA := d.search([]string{"zphloxalphaws"}, parseSearchSourceFused(t, resA.Content[0].Text))
	if srA.IsError || !strings.Contains(srA.Content[0].Text, "zphloxalphaws") {
		t.Fatalf("repoa retrieval failed: %+v", srA)
	}
	srB := d.search([]string{"zphloxbetaws"}, parseSearchSourceFused(t, resB.Content[0].Text))
	if srB.IsError || !strings.Contains(srB.Content[0].Text, "zphloxbetaws") {
		t.Fatalf("repob retrieval failed: %+v", srB)
	}
}

// run_command's cwd discovers the workspace it lands in — the shell substrate
// gets the same disambiguation as the structured tools.
func TestAdapterWorkspace_RunCommandCwdRouting(t *testing.T) {
	base := t.TempDir()
	repoa := gitRepoIT(t, base, "repoa")
	repob := gitRepoIT(t, base, "repob")
	const marker = "zphloxcwdroute"
	writeWorkspaceFile(t, repob, "note.txt", marker+" routed\n"+inlinePad)

	_, _, d := newAdapterLoop(t, repoa)

	res := d.callTool("calm_run_command", map[string]any{"command": "cat note.txt", "cwd": repob})
	if res.IsError {
		t.Fatalf("run_command errored: %+v", res)
	}
	base2 := parseSearchSource(t, res.Content[0].Text)
	if base2 != "calm:v1:file:read:repob:note.txt" {
		t.Fatalf("cwd-routed label = %q; want the repob segment", base2)
	}
}

// A VCS-less dependency store anchors on its manifest, and a basename with a
// reserved grammar character (module-cache name@version) percent-encodes in
// the label segment — raw '@' never enters a base label.
func TestAdapterWorkspace_ManifestStoreEncodes(t *testing.T) {
	base := t.TempDir()
	repoa := gitRepoIT(t, base, "repoa")
	lib := filepath.Join(base, "store", "lib@v1")
	if err := os.MkdirAll(lib, 0o750); err != nil {
		t.Fatalf("mkdir store: %v", err)
	}
	writeWorkspaceFile(t, lib, "go.mod", "module lib\n")
	writeWorkspaceFile(t, lib, "mod.txt", "zphloxmanifest store content\n"+inlinePad)

	_, _, d := newAdapterLoop(t, repoa)

	res := d.callTool("calm_read_file", map[string]any{"path": filepath.Join(lib, "mod.txt")})
	if res.IsError {
		t.Fatalf("store read errored: %+v", res)
	}
	got := parseSearchSource(t, res.Content[0].Text)
	if got != "calm:v1:file:read:lib%40v1:mod.txt" {
		t.Fatalf("store label = %q; want the %%40-encoded segment", got)
	}
}

func gitRepoIT(t *testing.T, parent, name string) string {
	t.Helper()
	dir := filepath.Join(parent, name)
	if err := os.MkdirAll(filepath.Join(dir, ".git"), 0o750); err != nil {
		t.Fatalf("mkdir %s: %v", dir, err)
	}
	return dir
}
