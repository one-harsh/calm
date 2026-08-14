// Copyright 2026 The CALM Authors
// SPDX-License-Identifier: Apache-2.0

package mcp_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/mock"

	"github.com/one-harsh/calm/internal/adapter/calm"
)

func sourcesCapture(m *calm.MockClient, times int) (*sync.Mutex, *[]string) {
	var mu sync.Mutex
	var sources []string
	m.EXPECT().Ingest(mock.Anything, "tok-1", mock.Anything).RunAndReturn(
		func(_ context.Context, _ string, in calm.IngestInput) (calm.IngestSummary, error) {
			mu.Lock()
			sources = append(sources, in.Source)
			mu.Unlock()
			return calm.IngestSummary{Source: in.Source, SectionsIndexed: 1, SectionsTotal: 1}, nil
		},
	).Times(times)
	return &mu, &sources
}

var (
	capturedLabelRe = regexp.MustCompile(`sections under "([^"]+)"`)
	basisLineRe     = regexp.MustCompile(`(?m)^basis=(\S+) `)
)

func readBasis(t *testing.T, h *harness, id int, m *calm.MockClient, path string) string {
	t.Helper()
	done := writeEventsSignal(m, nil)
	res := callTool(t, h, id, "calm_read_file", map[string]any{"path": path})
	if res.IsError {
		t.Fatalf("basis read errored: %+v", res)
	}
	awaitSignal(t, done)
	match := capturedLabelRe.FindStringSubmatch(resultText(t, res))
	if match == nil {
		t.Fatalf("read response carried no label to use as basis:\n%s", resultText(t, res))
	}
	return match[1]
}

func nextBasis(t *testing.T, text string) string {
	t.Helper()
	match := basisLineRe.FindStringSubmatch(text)
	if match == nil {
		t.Fatalf("response carried no successor basis:\n%s", text)
	}
	return match[1]
}

func TestEditFile_CurrentBasisAppliesAndHandsTheNextBasis(t *testing.T) {
	m := calm.NewMockClient(t)
	inspectSession(t, m)
	mu, sources := sourcesCapture(m, 3)

	ws := t.TempDir()
	writeWorkspaceFileMCP(t, ws, "f.txt", "hello old world\n")
	h := newWorkspaceHarness(t, m, ws)
	initSession(t, h, "claude-code")

	basis := readBasis(t, h, 2, m, "f.txt")
	if basis != "calm:v1:file:read:f.txt" && !strings.HasPrefix(basis, "calm:v1:file:read:f.txt@") {
		t.Fatalf("read basis = %q; want the fused read identity", basis)
	}

	eventsDone, events := eventCapture(m)
	res := callTool(t, h, 3, "calm_edit_file", map[string]any{
		"path": "f.txt", "basis": basis, "old_string": "old", "new_string": "new",
	})
	if res.IsError {
		t.Fatalf("edit errored: %+v", res)
	}
	text := resultText(t, res)
	if strings.Contains(text, "hello new world") {
		t.Errorf("success must not echo the edited content; got:\n%s", text)
	}
	if !strings.HasPrefix(text, "edited f.txt (16 bytes).") {
		t.Errorf("success must open with the bare confirmation; got:\n%s", text)
	}
	successor := nextBasis(t, text)
	if !strings.HasPrefix(successor, "calm:v1:file:read:f.txt@") || successor == basis {
		t.Errorf("successor basis = %q; want a fresh fused read label", successor)
	}

	disk, _ := os.ReadFile(filepath.Join(ws, "f.txt"))
	if string(disk) != "hello new world\n" {
		t.Errorf("disk = %q; want the edit applied", disk)
	}
	awaitSignal(t, eventsDone)

	mu.Lock()
	want := []string{"calm:v1:file:read:f.txt", "calm:v1:file:edit:f.txt#2", "calm:v1:file:read:f.txt"}
	if len(*sources) != 3 || (*sources)[1] != want[1] || (*sources)[2] != want[2] {
		t.Errorf("ingest order = %v; want read then history-then-latest %v", *sources, want)
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
	if ft["latest_source"] != "calm:v1:file:read:f.txt" || ft["history_source"] != "calm:v1:file:edit:f.txt#2" {
		t.Errorf("cross-links = %v / %v", ft["latest_source"], ft["history_source"])
	}
}

func TestEditFile_StaleBasisRejectsThenTheFreshBasisSucceeds(t *testing.T) {
	m := calm.NewMockClient(t)
	inspectSession(t, m)
	sourcesCapture(m, 4)

	ws := t.TempDir()
	path := filepath.Join(ws, "f.txt")
	writeWorkspaceFileMCP(t, ws, "f.txt", "hello old world\n")
	h := newWorkspaceHarness(t, m, ws)
	initSession(t, h, "claude-code")

	stale := readBasis(t, h, 2, m, "f.txt")

	// Something outside the adapter moves the file on.
	const drifted = "hello old world\nappended out of band\n"
	if err := os.WriteFile(path, []byte(drifted), 0o600); err != nil {
		t.Fatalf("out-of-band write: %v", err)
	}

	rejectEvents := writeEventsSignal(m, nil)
	res := callTool(t, h, 3, "calm_edit_file", map[string]any{
		"path": "f.txt", "basis": stale, "old_string": "old", "new_string": "new",
	})
	if !res.IsError {
		t.Fatalf("stale basis must reject: %+v", res)
	}
	text := resultText(t, res)
	if !strings.HasPrefix(text, "edit rejected: the file changed since basis "+stale+". f.txt is unchanged.") {
		t.Errorf("rejection must name the cause and the untouched file; got:\n%s", text)
	}
	if !strings.Contains(text, "Current state: 37 bytes, 2 lines.") {
		t.Errorf("rejection must carry the compact state summary; got:\n%s", text)
	}
	if disk, _ := os.ReadFile(path); string(disk) != drifted {
		t.Errorf("disk = %q; a rejected edit must not touch the file", disk)
	}
	awaitSignal(t, rejectEvents)

	fresh := nextBasis(t, text)
	if fresh == stale {
		t.Fatalf("rejection handed back the same stale basis %q", fresh)
	}

	retryEvents := writeEventsSignal(m, nil)
	res = callTool(t, h, 4, "calm_edit_file", map[string]any{
		"path": "f.txt", "basis": fresh, "old_string": "old", "new_string": "new",
	})
	if res.IsError {
		t.Fatalf("retry with the rejection's basis errored: %+v", res)
	}
	awaitSignal(t, retryEvents)
	if disk, _ := os.ReadFile(path); string(disk) != "hello new world\nappended out of band\n" {
		t.Errorf("disk = %q; want the retried edit applied", disk)
	}
}

func TestEditFile_UnknownBasisRejectsWithTheSameShape(t *testing.T) {
	m := calm.NewMockClient(t)
	inspectSession(t, m)
	sourcesCapture(m, 3)

	ws := t.TempDir()
	path := filepath.Join(ws, "f.txt")
	writeWorkspaceFileMCP(t, ws, "f.txt", "hello old world\n")
	h := newWorkspaceHarness(t, m, ws)
	initSession(t, h, "claude-code")

	const forged = "calm:v1:file:read:f.txt@zzzzzz"
	rejectEvents := writeEventsSignal(m, nil)
	res := callTool(t, h, 2, "calm_edit_file", map[string]any{
		"path": "f.txt", "basis": forged, "old_string": "old", "new_string": "new",
	})
	if !res.IsError {
		t.Fatalf("unknown basis must reject: %+v", res)
	}
	text := resultText(t, res)
	if !strings.HasPrefix(text, "edit rejected: basis "+forged+" is not a capture this session recorded for this file.") {
		t.Errorf("unknown-basis rejection must name the cause; got:\n%s", text)
	}
	if !strings.Contains(text, "Current state: 16 bytes, 1 lines.") {
		t.Errorf("unknown-basis rejection must carry the same state summary; got:\n%s", text)
	}
	if disk, _ := os.ReadFile(path); string(disk) != "hello old world\n" {
		t.Errorf("disk = %q; a rejected edit must not touch the file", disk)
	}
	awaitSignal(t, rejectEvents)

	retryEvents := writeEventsSignal(m, nil)
	res = callTool(t, h, 3, "calm_edit_file", map[string]any{
		"path": "f.txt", "basis": nextBasis(t, text), "old_string": "old", "new_string": "new",
	})
	if res.IsError {
		t.Fatalf("retry with the rejection's basis errored: %+v", res)
	}
	awaitSignal(t, retryEvents)
	if disk, _ := os.ReadFile(path); string(disk) != "hello new world\n" {
		t.Errorf("disk = %q; want the retried edit applied", disk)
	}
}

func TestEditFile_BasisFromAnotherFileRejects(t *testing.T) {
	m := calm.NewMockClient(t)
	inspectSession(t, m)
	sourcesCapture(m, 2)

	ws := t.TempDir()
	const shared = "hello old world\n"
	writeWorkspaceFileMCP(t, ws, "twin-a.txt", shared)
	writeWorkspaceFileMCP(t, ws, "twin-b.txt", shared)
	h := newWorkspaceHarness(t, m, ws)
	initSession(t, h, "claude-code")

	basisA := readBasis(t, h, 2, m, "twin-a.txt")

	rejectEvents := writeEventsSignal(m, nil)
	res := callTool(t, h, 3, "calm_edit_file", map[string]any{
		"path": "twin-b.txt", "basis": basisA, "old_string": "old", "new_string": "new",
	})
	if !res.IsError {
		t.Fatalf("another file's basis must reject: %+v", res)
	}
	if !strings.Contains(resultText(t, res), "is not a capture this session recorded for this file") {
		t.Errorf("rejection must say the basis belongs elsewhere; got:\n%s", resultText(t, res))
	}
	if disk, _ := os.ReadFile(filepath.Join(ws, "twin-b.txt")); string(disk) != shared {
		t.Errorf("disk = %q; a rejected edit must not touch the file", disk)
	}
	awaitSignal(t, rejectEvents)
}

func TestEditFile_DegradedCaptureStillAppliesTheVerifiedEdit(t *testing.T) {
	m := calm.NewMockClient(t)
	inspectSession(t, m)
	m.EXPECT().Ingest(mock.Anything, "tok-1", mock.Anything).Return(
		calm.IngestSummary{Source: "calm:v1:file:read:f.txt", SectionsIndexed: 1, SectionsTotal: 1}, nil,
	).Once()
	m.EXPECT().Ingest(mock.Anything, "tok-1", mock.Anything).Return(
		calm.IngestSummary{}, errors.New("ingest unavailable"),
	).Times(2)

	ws := t.TempDir()
	path := filepath.Join(ws, "f.txt")
	writeWorkspaceFileMCP(t, ws, "f.txt", "hello old world\n")
	h := newWorkspaceHarness(t, m, ws)
	initSession(t, h, "claude-code")

	basis := readBasis(t, h, 2, m, "f.txt")

	degradedEvents := writeEventsSignal(m, nil)
	res := callTool(t, h, 3, "calm_edit_file", map[string]any{
		"path": "f.txt", "basis": basis, "old_string": "old", "new_string": "new",
	})
	if res.IsError {
		t.Fatalf("a verified edit must still apply while capture is degraded: %+v", res)
	}
	awaitSignal(t, degradedEvents)
	if disk, _ := os.ReadFile(path); string(disk) != "hello new world\n" {
		t.Errorf("disk = %q; want the edit applied despite degraded capture", disk)
	}
	text := resultText(t, res)
	if !strings.Contains(text, "edited f.txt (16 bytes).") {
		t.Errorf("degraded success must still confirm the mutation; got:\n%s", text)
	}
	if !strings.Contains(text, "no basis label was minted") {
		t.Errorf("degraded success must say no successor basis exists; got:\n%s", text)
	}
	if basisLineRe.MatchString(text) {
		t.Errorf("degraded success must not advertise a basis; got:\n%s", text)
	}
	if !strings.Contains(text, "CALM degraded — capture_failed.") {
		t.Errorf("degraded success must carry the canonical degradation phrase; got:\n%s", text)
	}
}

func TestEditFile_NonUniqueMatchFails(t *testing.T) {
	m := calm.NewMockClient(t)
	inspectSession(t, m)
	sourcesCapture(m, 1)

	ws := t.TempDir()
	const content = "dup line\ndup line\nunique\n"
	writeWorkspaceFileMCP(t, ws, "f.txt", content)
	h := newWorkspaceHarness(t, m, ws)
	initSession(t, h, "claude-code")

	basis := readBasis(t, h, 2, m, "f.txt")

	res := callTool(t, h, 3, "calm_edit_file", map[string]any{
		"path": "f.txt", "basis": basis, "old_string": "dup line", "new_string": "x",
	})
	if !res.IsError || !strings.Contains(resultText(t, res), "matches 2 times") {
		t.Errorf("two matches = %+v; want count-bearing failure", res)
	}

	res = callTool(t, h, 4, "calm_edit_file", map[string]any{
		"path": "f.txt", "basis": basis, "old_string": "absent", "new_string": "x",
	})
	if !res.IsError || !strings.Contains(resultText(t, res), "matches 0 times") {
		t.Errorf("zero matches = %+v; want count-bearing failure", res)
	}

	disk, _ := os.ReadFile(filepath.Join(ws, "f.txt"))
	if string(disk) != content {
		t.Errorf("file changed on failed edits: %q", disk)
	}
}

func TestEditFile_ReplaceAllReplacesEveryOccurrenceAndReportsTheCount(t *testing.T) {
	m := calm.NewMockClient(t)
	inspectSession(t, m)
	mu, sources := sourcesCapture(m, 3)

	ws := t.TempDir()
	writeWorkspaceFileMCP(t, ws, "f.txt", "dup\ndup\ndup\n")
	h := newWorkspaceHarness(t, m, ws)
	initSession(t, h, "claude-code")

	basis := readBasis(t, h, 2, m, "f.txt")

	eventsDone, events := eventCapture(m)
	res := callTool(t, h, 3, "calm_edit_file", map[string]any{
		"path": "f.txt", "basis": basis, "old_string": "dup", "new_string": "x", "replace_all": true,
	})
	if res.IsError {
		t.Fatalf("replace_all edit errored: %+v", res)
	}
	text := resultText(t, res)
	if !strings.HasPrefix(text, "edited f.txt (6 bytes), replaced 3 occurrences.\n") {
		t.Errorf("replace_all must report the count on the bare confirmation; got:\n%s", text)
	}
	if strings.Contains(text, "x\nx\nx") {
		t.Errorf("success must not echo the edited content; got:\n%s", text)
	}
	successor := nextBasis(t, text)
	if !strings.HasPrefix(successor, "calm:v1:file:read:f.txt@") || successor == basis {
		t.Errorf("successor basis = %q; want a fresh fused read label", successor)
	}

	disk, _ := os.ReadFile(filepath.Join(ws, "f.txt"))
	if string(disk) != "x\nx\nx\n" {
		t.Errorf("disk = %q; want every occurrence replaced", disk)
	}
	awaitSignal(t, eventsDone)

	mu.Lock()
	want := []string{"calm:v1:file:read:f.txt", "calm:v1:file:edit:f.txt#2", "calm:v1:file:read:f.txt"}
	if len(*sources) != 3 || (*sources)[1] != want[1] || (*sources)[2] != want[2] {
		t.Errorf("ingest order = %v; want one read then a single history-then-latest pair %v", *sources, want)
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
	if !strings.Contains(diff, "-dup") || !strings.Contains(diff, "+x") {
		t.Errorf("diff = %q; want the replacements reflected", diff)
	}
	if ft["latest_source"] != "calm:v1:file:read:f.txt" || ft["history_source"] != "calm:v1:file:edit:f.txt#2" {
		t.Errorf("cross-links = %v / %v", ft["latest_source"], ft["history_source"])
	}
}

func TestEditFile_ReplaceAllSingleMatchReportsOneOccurrence(t *testing.T) {
	m := calm.NewMockClient(t)
	inspectSession(t, m)
	sourcesCapture(m, 3)

	ws := t.TempDir()
	writeWorkspaceFileMCP(t, ws, "f.txt", "hello old world\n")
	h := newWorkspaceHarness(t, m, ws)
	initSession(t, h, "claude-code")

	basis := readBasis(t, h, 2, m, "f.txt")

	eventsDone := writeEventsSignal(m, nil)
	res := callTool(t, h, 3, "calm_edit_file", map[string]any{
		"path": "f.txt", "basis": basis, "old_string": "old", "new_string": "new", "replace_all": true,
	})
	if res.IsError {
		t.Fatalf("replace_all with a single match errored: %+v", res)
	}
	text := resultText(t, res)
	if !strings.HasPrefix(text, "edited f.txt (16 bytes), replaced 1 occurrence.\n") {
		t.Errorf("a lone match must be counted in the singular; got:\n%s", text)
	}
	if successor := nextBasis(t, text); !strings.HasPrefix(successor, "calm:v1:file:read:f.txt@") || successor == basis {
		t.Errorf("successor basis = %q; want a fresh fused read label", successor)
	}
	awaitSignal(t, eventsDone)
	if disk, _ := os.ReadFile(filepath.Join(ws, "f.txt")); string(disk) != "hello new world\n" {
		t.Errorf("disk = %q; want the single occurrence replaced", disk)
	}
}

func TestEditFile_ReplaceAllZeroMatchFails(t *testing.T) {
	m := calm.NewMockClient(t)
	inspectSession(t, m)
	sourcesCapture(m, 1)

	ws := t.TempDir()
	const content = "nothing to see here\n"
	writeWorkspaceFileMCP(t, ws, "f.txt", content)
	h := newWorkspaceHarness(t, m, ws)
	initSession(t, h, "claude-code")

	basis := readBasis(t, h, 2, m, "f.txt")

	res := callTool(t, h, 3, "calm_edit_file", map[string]any{
		"path": "f.txt", "basis": basis, "old_string": "absent", "new_string": "x", "replace_all": true,
	})
	if !res.IsError || !strings.Contains(resultText(t, res), "matches 0 times") {
		t.Errorf("zero-match replace_all = %+v; want a count-bearing failure", res)
	}
	if disk, _ := os.ReadFile(filepath.Join(ws, "f.txt")); string(disk) != content {
		t.Errorf("file changed on a zero-match replace_all: %q", disk)
	}
}

func TestEditFile_ReplaceAllFalseKeepsExactOnce(t *testing.T) {
	m := calm.NewMockClient(t)
	inspectSession(t, m)
	sourcesCapture(m, 3)

	ws := t.TempDir()
	writeWorkspaceFileMCP(t, ws, "f.txt", "dup line\ndup line\nunique\n")
	h := newWorkspaceHarness(t, m, ws)
	initSession(t, h, "claude-code")

	basis := readBasis(t, h, 2, m, "f.txt")

	res := callTool(t, h, 3, "calm_edit_file", map[string]any{
		"path": "f.txt", "basis": basis, "old_string": "dup line", "new_string": "x", "replace_all": false,
	})
	text := resultText(t, res)
	if !res.IsError || !strings.Contains(text, "matches 2 times") {
		t.Errorf("multiple matches with the flag off = %+v; want the count-bearing failure", res)
	}
	if !strings.Contains(text, "set replace_all=true") {
		t.Errorf("the failure must name the option that would apply the edit; got:\n%s", text)
	}

	eventsDone := writeEventsSignal(m, nil)
	res = callTool(t, h, 4, "calm_edit_file", map[string]any{
		"path": "f.txt", "basis": basis, "old_string": "unique", "new_string": "solo", "replace_all": false,
	})
	if res.IsError {
		t.Fatalf("single match with the flag off errored: %+v", res)
	}
	if text = resultText(t, res); !strings.HasPrefix(text, "edited f.txt (23 bytes).\n") {
		t.Errorf("the flag-off confirmation must stay bare, with no count; got:\n%s", text)
	}
	awaitSignal(t, eventsDone)
	if disk, _ := os.ReadFile(filepath.Join(ws, "f.txt")); string(disk) != "dup line\ndup line\nsolo\n" {
		t.Errorf("disk = %q; want only the unique match replaced", disk)
	}
}

func TestEditFile_CRLFRoundtripPreserved(t *testing.T) {
	m := calm.NewMockClient(t)
	inspectSession(t, m)
	sourcesCapture(m, 3)

	ws := t.TempDir()
	writeWorkspaceFileMCP(t, ws, "win.txt", "alpha\r\nbeta\r\ngamma\r\n")
	h := newWorkspaceHarness(t, m, ws)
	initSession(t, h, "claude-code")

	basis := readBasis(t, h, 2, m, "win.txt")

	// LF-only old_string must NOT match the CRLF file.
	res := callTool(t, h, 3, "calm_edit_file", map[string]any{
		"path": "win.txt", "basis": basis, "old_string": "alpha\nbeta", "new_string": "x",
	})
	if !res.IsError || !strings.Contains(resultText(t, res), "matches 0 times") {
		t.Fatalf("LF old_string against CRLF file = %+v; want 0-match failure", res)
	}

	// CRLF old_string matches; CRLF survives verbatim.
	eventsDone := writeEventsSignal(m, nil)
	res = callTool(t, h, 4, "calm_edit_file", map[string]any{
		"path": "win.txt", "basis": basis, "old_string": "beta\r\n", "new_string": "BETA\r\n",
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
	sourcesCapture(m, 3)

	ws := t.TempDir()
	path := filepath.Join(ws, "run.sh")
	if err := os.WriteFile(path, []byte("echo old\n"), 0o755); err != nil {
		t.Fatalf("fixture: %v", err)
	}
	h := newWorkspaceHarness(t, m, ws)
	initSession(t, h, "claude-code")

	basis := readBasis(t, h, 2, m, "run.sh")
	eventsDone := writeEventsSignal(m, nil)
	if res := callTool(t, h, 3, "calm_edit_file", map[string]any{
		"path": "run.sh", "basis": basis, "old_string": "old", "new_string": "new",
	}); res.IsError {
		t.Fatalf("edit errored: %+v", res)
	}
	fi, _ := os.Stat(path)
	if fi.Mode().Perm() != 0o755 {
		t.Errorf("mode = %o; want 0755 preserved", fi.Mode().Perm())
	}
	awaitSignal(t, eventsDone)
}

func TestWriteFile_CreateAbsentNeedsNoBasis(t *testing.T) {
	m := calm.NewMockClient(t)
	inspectSession(t, m)
	sourcesCapture(m, 2)
	eventsDone, events := eventCapture(m)

	ws := t.TempDir()
	h := newWorkspaceHarness(t, m, ws)
	initSession(t, h, "claude-code")

	res := callTool(t, h, 2, "calm_write_file", map[string]any{
		"path": "fresh.txt", "content": "first\n",
	})
	if res.IsError {
		t.Fatalf("create errored: %+v", res)
	}
	text := resultText(t, res)
	if !strings.HasPrefix(text, "created fresh.txt (6 bytes).") {
		t.Errorf("create must open with the bare confirmation; got:\n%s", text)
	}
	if !strings.HasPrefix(nextBasis(t, text), "calm:v1:file:read:fresh.txt@") {
		t.Errorf("create must hand back the read identity as the next basis; got:\n%s", text)
	}
	fi, err := os.Stat(filepath.Join(ws, "fresh.txt"))
	if err != nil {
		t.Fatalf("created file missing: %v", err)
	}
	if runtime.GOOS != "windows" && fi.Mode().Perm() != 0o600 {
		t.Errorf("create mode = %o; want 0600", fi.Mode().Perm())
	}
	awaitSignal(t, eventsDone)

	var op string
	for _, e := range *events {
		if e.Type == "file_touched" {
			op, _ = e.Data["operation"].(string)
		}
	}
	if op != "create" {
		t.Errorf("operation = %q; want create", op)
	}
}

func TestWriteFile_ExistingTargetRequiresCurrentBasis(t *testing.T) {
	m := calm.NewMockClient(t)
	inspectSession(t, m)
	sourcesCapture(m, 4)

	ws := t.TempDir()
	path := filepath.Join(ws, "held.txt")
	writeWorkspaceFileMCP(t, ws, "held.txt", "occupied\n")
	h := newWorkspaceHarness(t, m, ws)
	initSession(t, h, "claude-code")

	noBasisEvents := writeEventsSignal(m, nil)
	res := callTool(t, h, 2, "calm_write_file", map[string]any{
		"path": "held.txt", "content": "clobbered\n",
	})
	if !res.IsError {
		t.Fatalf("write over an existing file without a basis must reject: %+v", res)
	}
	text := resultText(t, res)
	if !strings.HasPrefix(text, "write rejected: no basis was supplied and the file already exists. held.txt is unchanged.") {
		t.Errorf("rejection must name the unexpectedly-present file; got:\n%s", text)
	}
	if disk, _ := os.ReadFile(path); string(disk) != "occupied\n" {
		t.Errorf("disk = %q; a rejected write must not touch the file", disk)
	}
	awaitSignal(t, noBasisEvents)
	stale := nextBasis(t, text)

	if err := os.WriteFile(path, []byte("occupied elsewhere\n"), 0o600); err != nil {
		t.Fatalf("out-of-band write: %v", err)
	}
	staleEvents := writeEventsSignal(m, nil)
	res = callTool(t, h, 3, "calm_write_file", map[string]any{
		"path": "held.txt", "content": "clobbered\n", "basis": stale,
	})
	if !res.IsError || !strings.Contains(resultText(t, res), "the file changed since basis "+stale) {
		t.Fatalf("stale basis must reject: %+v", res)
	}
	if disk, _ := os.ReadFile(path); string(disk) != "occupied elsewhere\n" {
		t.Errorf("disk = %q; a rejected write must not touch the file", disk)
	}
	awaitSignal(t, staleEvents)

	// The stale rejection's basis is current but unread; the overwrite unlocks
	// only after the label is read back through this shell.
	current := nextBasis(t, resultText(t, res))
	m.EXPECT().Search(mock.Anything, mock.Anything, mock.Anything).Return(oneHit(""), nil).Once()
	if sres := callTool(t, h, 4, "calm_search", map[string]any{"source": current}); sres.IsError {
		t.Fatalf("read-back search errored: %+v", sres)
	}

	currentEvents := writeEventsSignal(m, nil)
	res = callTool(t, h, 5, "calm_write_file", map[string]any{
		"path": "held.txt", "content": "clobbered\n", "basis": current,
	})
	if res.IsError {
		t.Fatalf("write with a current basis errored: %+v", res)
	}
	if !strings.HasPrefix(resultText(t, res), "wrote held.txt (10 bytes).") {
		t.Errorf("overwrite must confirm bare; got:\n%s", resultText(t, res))
	}
	awaitSignal(t, currentEvents)
	if disk, _ := os.ReadFile(path); string(disk) != "clobbered\n" {
		t.Errorf("disk = %q; want the verified overwrite applied", disk)
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
		"path": "big.txt", "basis": "calm:v1:file:read:big.txt@zzzzzz", "old_string": "x", "new_string": "y",
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

func TestEditFile_SecretRedactedInEventDiff(t *testing.T) {
	m := calm.NewMockClient(t)
	inspectSession(t, m)
	sourcesCapture(m, 3)

	ws := t.TempDir()
	writeWorkspaceFileMCP(t, ws, "conf.txt", "flag=placeholder\n")
	h := newWorkspaceHarness(t, m, ws)
	initSession(t, h, "claude-code")

	basis := readBasis(t, h, 2, m, "conf.txt")
	eventsDone, events := eventCapture(m)
	if res := callTool(t, h, 3, "calm_edit_file", map[string]any{
		"path": "conf.txt", "basis": basis, "old_string": "placeholder", "new_string": "--password=hunter2",
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
		{"blank path", "calm_edit_file", map[string]any{"path": " ", "basis": "b", "old_string": "a", "new_string": "b"}},
		{"empty old_string", "calm_edit_file", map[string]any{"path": "f", "basis": "b", "old_string": "", "new_string": "b"}},
		{"old equals new", "calm_edit_file", map[string]any{"path": "f", "basis": "b", "old_string": "a", "new_string": "a"}},
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

func TestWriteFile_RejectionBasisNeedsReadBackBeforeOverwrite(t *testing.T) {
	m := calm.NewMockClient(t)
	inspectSession(t, m)
	sourcesCapture(m, 4)

	ws := t.TempDir()
	path := filepath.Join(ws, "f.txt")
	writeWorkspaceFileMCP(t, ws, "f.txt", "hello old world\n")
	h := newWorkspaceHarness(t, m, ws)
	initSession(t, h, "claude-code")

	stale := readBasis(t, h, 2, m, "f.txt")
	const drifted = "hello old world\nappended out of band\n"
	if err := os.WriteFile(path, []byte(drifted), 0o600); err != nil {
		t.Fatalf("out-of-band write: %v", err)
	}

	rejectEvents := writeEventsSignal(m, nil)
	res := callTool(t, h, 3, "calm_write_file", map[string]any{
		"path": "f.txt", "basis": stale, "content": "mine, wholesale\n",
	})
	if !res.IsError {
		t.Fatalf("stale basis must reject: %+v", res)
	}
	awaitSignal(t, rejectEvents)
	fresh := nextBasis(t, resultText(t, res))

	res = callTool(t, h, 4, "calm_write_file", map[string]any{
		"path": "f.txt", "basis": fresh, "content": "mine, wholesale\n",
	})
	if !res.IsError {
		t.Fatalf("unread rejection basis must refuse the overwrite: %+v", res)
	}
	text := resultText(t, res)
	if !strings.Contains(text, "has not been read") || !strings.Contains(text, "calm_search source="+fresh) {
		t.Errorf("refusal must name the read that unlocks it; got:\n%s", text)
	}
	if disk, _ := os.ReadFile(path); string(disk) != drifted {
		t.Errorf("disk = %q; a refused overwrite must not touch the file", disk)
	}

	// A ranked, source-scoped query must NOT clear the read guard — snippets are
	// not the content; only the document-order reread that follows unlocks it.
	m.EXPECT().Search(mock.Anything, mock.Anything, mock.Anything).Return(oneHit("peek"), nil).Once()
	if sres := callTool(t, h, 5, "calm_search", map[string]any{"source": fresh, "queries": []string{"peek"}}); sres.IsError {
		t.Fatalf("ranked peek errored: %+v", sres)
	}
	res = callTool(t, h, 6, "calm_write_file", map[string]any{
		"path": "f.txt", "basis": fresh, "content": "mine, wholesale\n",
	})
	if !res.IsError || !strings.Contains(resultText(t, res), "has not been read") {
		t.Fatalf("a ranked peek must not unlock the overwrite: %+v", res)
	}

	m.EXPECT().Search(mock.Anything, mock.Anything, mock.Anything).Return(oneHit(""), nil).Once()
	if sres := callTool(t, h, 7, "calm_search", map[string]any{"source": fresh}); sres.IsError {
		t.Fatalf("read-back search errored: %+v", sres)
	}

	applyEvents := writeEventsSignal(m, nil)
	res = callTool(t, h, 8, "calm_write_file", map[string]any{
		"path": "f.txt", "basis": fresh, "content": "mine, wholesale\n",
	})
	if res.IsError {
		t.Fatalf("write after read-back errored: %+v", res)
	}
	awaitSignal(t, applyEvents)
	if disk, _ := os.ReadFile(path); string(disk) != "mine, wholesale\n" {
		t.Errorf("disk = %q; want the overwrite applied after read-back", disk)
	}
}

// A basis names a file the caller believes exists. Recreating over an
// out-of-band deletion would silently undo it, so the write discloses the
// deletion; a basis-less retry is the explicit create.
func TestWriteFile_DeletedTargetWithBasisRejects(t *testing.T) {
	m := calm.NewMockClient(t)
	inspectSession(t, m)
	sourcesCapture(m, 3)

	ws := t.TempDir()
	path := filepath.Join(ws, "f.txt")
	writeWorkspaceFileMCP(t, ws, "f.txt", "here today\n")
	h := newWorkspaceHarness(t, m, ws)
	initSession(t, h, "claude-code")

	basis := readBasis(t, h, 2, m, "f.txt")
	if err := os.Remove(path); err != nil {
		t.Fatalf("out-of-band delete: %v", err)
	}

	res := callTool(t, h, 3, "calm_write_file", map[string]any{
		"path": "f.txt", "basis": basis, "content": "resurrected\n",
	})
	if !res.IsError {
		t.Fatalf("write with a basis for a deleted file must reject: %+v", res)
	}
	text := resultText(t, res)
	if !strings.Contains(text, "was deleted since basis "+basis) || !strings.Contains(text, "retry without basis") {
		t.Errorf("rejection must disclose the deletion and name the retry; got:\n%s", text)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("a rejected write must not resurrect the file")
	}

	createEvents := writeEventsSignal(m, nil)
	res = callTool(t, h, 4, "calm_write_file", map[string]any{
		"path": "f.txt", "content": "resurrected\n",
	})
	if res.IsError {
		t.Fatalf("basis-less create errored: %+v", res)
	}
	awaitSignal(t, createEvents)
	if disk, _ := os.ReadFile(path); string(disk) != "resurrected\n" {
		t.Errorf("disk = %q; want the explicit create applied", disk)
	}
}
