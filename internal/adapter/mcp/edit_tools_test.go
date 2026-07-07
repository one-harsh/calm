// Copyright 2026 The CALM Authors
// SPDX-License-Identifier: Apache-2.0

package mcp_test

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/mock"

	"github.com/one-harsh/calm/internal/adapter/calm"
)

// sourcesCapture records ingest sources in call order for dual-write assertions.
func sourcesCapture(m *calm.MockClient, times int) (*sync.Mutex, *[]string) {
	var mu sync.Mutex
	var sources []string
	m.EXPECT().Ingest(mock.Anything, "tok-1", mock.Anything).RunAndReturn(
		func(_ context.Context, _ string, in calm.IngestInput) (calm.IngestSummary, error) {
			mu.Lock()
			sources = append(sources, in.Source)
			mu.Unlock()
			return calm.IngestSummary{Source: in.Source, SectionsIndexed: 1, SectionsTotal: 1}, nil
		}).Times(times)
	return &mu, &sources
}

// The edit happy path: history-then-latest dual write, verbatim echo of the
// post-edit content, disk updated, and a file_touched event carrying the
// operation, diff, and both cross-links.
func TestEditFile_HappyPathDualWriteAndEvent(t *testing.T) {
	m := calm.NewMockClient(t)
	inspectSession(t, m)
	mu, sources := sourcesCapture(m, 2)
	eventsDone, events := eventCapture(m)

	ws := t.TempDir()
	writeWorkspaceFileMCP(t, ws, "f.txt", "hello old world\n")
	h := newWorkspaceHarness(t, m, ws)
	initSession(t, h, "claude-code")

	res := callTool(t, h, 2, "calm_edit_file", map[string]any{
		"path": "f.txt", "old_string": "old", "new_string": "new",
	})
	if res.IsError {
		t.Fatalf("edit errored: %+v", res)
	}
	if got := resultText(t, res); got != "hello new world\n" {
		t.Errorf("visible = %q; want post-edit content verbatim", got)
	}
	disk, _ := os.ReadFile(filepath.Join(ws, "f.txt"))
	if string(disk) != "hello new world\n" {
		t.Errorf("disk = %q; want the edit applied", disk)
	}
	awaitSignal(t, eventsDone)

	mu.Lock()
	want := []string{"calm:v1:file:edit:f.txt#1", "calm:v1:file:read:f.txt"}
	if len(*sources) != 2 || (*sources)[0] != want[0] || (*sources)[1] != want[1] {
		t.Errorf("ingest order = %v; want history-then-latest %v", *sources, want)
	}
	mu.Unlock()

	var ft map[string]any
	for _, e := range *events {
		if e.Type == "file_touched" {
			ft = e.Data
		}
	}
	if ft == nil {
		t.Fatalf("no file_touched event: %+v", *events)
	}
	if ft["operation"] != "edit" || ft["path"] != "f.txt" {
		t.Errorf("payload = %+v", ft)
	}
	diff, _ := ft["diff"].(string)
	if !strings.Contains(diff, "-hello old world") || !strings.Contains(diff, "+hello new world") {
		t.Errorf("diff = %q", diff)
	}
	if ft["latest_source"] != "calm:v1:file:read:f.txt" || ft["history_source"] != "calm:v1:file:edit:f.txt#1" {
		t.Errorf("cross-links = %v / %v", ft["latest_source"], ft["history_source"])
	}
}

// Exactly-once matching: zero and multiple occurrences both fail with the
// count, and the file is untouched (strict mocks prove no capture).
func TestEditFile_NonUniqueMatchFails(t *testing.T) {
	m := calm.NewMockClient(t)
	inspectSession(t, m)
	ws := t.TempDir()
	const content = "dup line\ndup line\nunique\n"
	writeWorkspaceFileMCP(t, ws, "f.txt", content)
	h := newWorkspaceHarness(t, m, ws)
	initSession(t, h, "claude-code")

	res := callTool(t, h, 2, "calm_edit_file", map[string]any{
		"path": "f.txt", "old_string": "dup line", "new_string": "x",
	})
	if !res.IsError || !strings.Contains(resultText(t, res), "matches 2 times") {
		t.Errorf("two matches = %+v; want count-bearing failure", res)
	}

	res = callTool(t, h, 3, "calm_edit_file", map[string]any{
		"path": "f.txt", "old_string": "absent", "new_string": "x",
	})
	if !res.IsError || !strings.Contains(resultText(t, res), "matches 0 times") {
		t.Errorf("zero matches = %+v; want count-bearing failure", res)
	}

	disk, _ := os.ReadFile(filepath.Join(ws, "f.txt"))
	if string(disk) != content {
		t.Errorf("file changed on failed edits: %q", disk)
	}
}

// Byte fidelity: CRLF content matches only CRLF-carrying old_string and the
// line endings survive the roundtrip byte-for-byte — no EOL normalization.
func TestEditFile_CRLFRoundtripPreserved(t *testing.T) {
	m := calm.NewMockClient(t)
	inspectSession(t, m)
	mu, _ := sourcesCapture(m, 2)
	_ = mu
	eventsDone, _ := eventCapture(m)

	ws := t.TempDir()
	writeWorkspaceFileMCP(t, ws, "win.txt", "alpha\r\nbeta\r\ngamma\r\n")
	h := newWorkspaceHarness(t, m, ws)
	initSession(t, h, "claude-code")

	// LF-only old_string must NOT match the CRLF file.
	res := callTool(t, h, 2, "calm_edit_file", map[string]any{
		"path": "win.txt", "old_string": "alpha\nbeta", "new_string": "x",
	})
	if !res.IsError || !strings.Contains(resultText(t, res), "matches 0 times") {
		t.Fatalf("LF old_string against CRLF file = %+v; want 0-match failure", res)
	}

	// CRLF old_string matches; CRLF survives verbatim.
	res = callTool(t, h, 3, "calm_edit_file", map[string]any{
		"path": "win.txt", "old_string": "beta\r\n", "new_string": "BETA\r\n",
	})
	if res.IsError {
		t.Fatalf("CRLF edit errored: %+v", res)
	}
	disk, _ := os.ReadFile(filepath.Join(ws, "win.txt"))
	if string(disk) != "alpha\r\nBETA\r\ngamma\r\n" {
		t.Errorf("disk = %q; want CRLF preserved byte-for-byte", disk)
	}
	awaitSignal(t, eventsDone)
}

func TestEditFile_ModePreserved(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("unix permission semantics")
	}
	m := calm.NewMockClient(t)
	inspectSession(t, m)
	sourcesCapture(m, 2)
	eventsDone, _ := eventCapture(m)

	ws := t.TempDir()
	path := filepath.Join(ws, "run.sh")
	if err := os.WriteFile(path, []byte("echo old\n"), 0o755); err != nil {
		t.Fatalf("fixture: %v", err)
	}
	h := newWorkspaceHarness(t, m, ws)
	initSession(t, h, "claude-code")

	if res := callTool(t, h, 2, "calm_edit_file", map[string]any{
		"path": "run.sh", "old_string": "old", "new_string": "new",
	}); res.IsError {
		t.Fatalf("edit errored: %+v", res)
	}
	fi, _ := os.Stat(path)
	if fi.Mode().Perm() != 0o755 {
		t.Errorf("mode = %o; want 0755 preserved", fi.Mode().Perm())
	}
	awaitSignal(t, eventsDone)
}

// write_file to an absent path creates it (0600, operation=create) and to an
// existing path overwrites (operation=write).
func TestWriteFile_CreateAndOverwrite(t *testing.T) {
	m := calm.NewMockClient(t)
	inspectSession(t, m)
	sourcesCapture(m, 4)
	var wg sync.WaitGroup
	wg.Add(2)
	var muEv sync.Mutex
	var ops []string
	m.EXPECT().WriteEvents(mock.Anything, mock.Anything, mock.Anything).
		RunAndReturn(func(_ context.Context, _ string, ev []calm.EventInput) error {
			muEv.Lock()
			for _, e := range ev {
				if e.Type == "file_touched" {
					ops = append(ops, e.Data["operation"].(string))
				}
			}
			muEv.Unlock()
			wg.Done()
			return nil
		}).Times(2)

	ws := t.TempDir()
	h := newWorkspaceHarness(t, m, ws)
	initSession(t, h, "claude-code")

	if res := callTool(t, h, 2, "calm_write_file", map[string]any{
		"path": "fresh.txt", "content": "first\n",
	}); res.IsError {
		t.Fatalf("create errored: %+v", res)
	}
	fi, err := os.Stat(filepath.Join(ws, "fresh.txt"))
	if err != nil {
		t.Fatalf("created file missing: %v", err)
	}
	if runtime.GOOS != "windows" && fi.Mode().Perm() != 0o600 {
		t.Errorf("create mode = %o; want 0600", fi.Mode().Perm())
	}

	if res := callTool(t, h, 3, "calm_write_file", map[string]any{
		"path": "fresh.txt", "content": "second\n",
	}); res.IsError {
		t.Fatalf("overwrite errored: %+v", res)
	}
	disk, _ := os.ReadFile(filepath.Join(ws, "fresh.txt"))
	if string(disk) != "second\n" {
		t.Errorf("disk = %q; want overwritten content", disk)
	}
	wg.Wait()

	muEv.Lock()
	defer muEv.Unlock()
	if len(ops) != 2 || ops[0] != "create" || ops[1] != "write" {
		t.Errorf("operations = %v; want [create write]", ops)
	}
}

func TestEditWrite_OversizeRefusals(t *testing.T) {
	m := calm.NewMockClient(t)
	inspectSession(t, m)
	ws := t.TempDir()
	big := strings.Repeat("x", 512*1024+1)
	writeWorkspaceFileMCP(t, ws, "big.txt", big)
	h := newWorkspaceHarness(t, m, ws)
	initSession(t, h, "claude-code")

	res := callTool(t, h, 2, "calm_edit_file", map[string]any{
		"path": "big.txt", "old_string": "x", "new_string": "y",
	})
	if !res.IsError || !strings.Contains(resultText(t, res), "capture limit") {
		t.Errorf("oversize edit = %+v; want capture-limit refusal", res)
	}

	res = callTool(t, h, 3, "calm_write_file", map[string]any{
		"path": "new.txt", "content": big,
	})
	if !res.IsError || !strings.Contains(resultText(t, res), "capture limit") {
		t.Errorf("oversize write = %+v; want capture-limit refusal", res)
	}
	if _, err := os.Stat(filepath.Join(ws, "new.txt")); !os.IsNotExist(err) {
		t.Errorf("oversize write created the file anyway")
	}
}

// A secret introduced by the edit is redacted in the event diff — the
// captured sources stay raw, the event metadata never carries the literal.
func TestEditFile_SecretRedactedInEventDiff(t *testing.T) {
	m := calm.NewMockClient(t)
	inspectSession(t, m)
	sourcesCapture(m, 2)
	eventsDone, events := eventCapture(m)

	ws := t.TempDir()
	writeWorkspaceFileMCP(t, ws, "conf.txt", "flag=placeholder\n")
	h := newWorkspaceHarness(t, m, ws)
	initSession(t, h, "claude-code")

	if res := callTool(t, h, 2, "calm_edit_file", map[string]any{
		"path": "conf.txt", "old_string": "placeholder", "new_string": "--password=hunter2",
	}); res.IsError {
		t.Fatalf("edit errored: %+v", res)
	}
	awaitSignal(t, eventsDone)

	for _, e := range *events {
		if e.Type != "file_touched" {
			continue
		}
		diff, _ := e.Data["diff"].(string)
		if strings.Contains(diff, "hunter2") {
			t.Errorf("secret literal in event diff: %q", diff)
		}
		if !strings.Contains(diff, "<redacted>") {
			t.Errorf("no redaction marker in diff: %q", diff)
		}
	}
}

func TestEditWrite_ArgErrors(t *testing.T) {
	m := calm.NewMockClient(t)
	inspectSession(t, m)
	h := newWorkspaceHarness(t, m, t.TempDir())
	initSession(t, h, "claude-code")

	cases := []struct {
		name string
		tool string
		args map[string]any
	}{
		{"blank path", "calm_edit_file", map[string]any{"path": " ", "old_string": "a", "new_string": "b"}},
		{"empty old_string", "calm_edit_file", map[string]any{"path": "f", "old_string": "", "new_string": "b"}},
		{"old equals new", "calm_edit_file", map[string]any{"path": "f", "old_string": "a", "new_string": "a"}},
		{"blank write path", "calm_write_file", map[string]any{"path": "", "content": "x"}},
	}
	for i, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			res := callTool(t, h, 2+i, c.tool, c.args)
			if !res.IsError || !strings.Contains(resultText(t, res), "invalid arguments") {
				t.Errorf("%s = %+v; want ArgError result", c.name, res)
			}
		})
	}
}
