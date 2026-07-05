// Copyright 2026 The CALM Authors
// SPDX-License-Identifier: Apache-2.0

package mcp_test

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	stdexec "os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/mock"

	"github.com/one-harsh/calm/internal/adapter/calm"
	"github.com/one-harsh/calm/internal/adapter/mcp"
)

func callTool(t *testing.T, h *harness, id int, name string, args map[string]any) mcp.ToolResult {
	t.Helper()
	h.send(req(id, "tools/call", map[string]any{"name": name, "arguments": args}))
	r := h.recv()
	if r.Error != nil {
		t.Fatalf("tools/call protocol error: %+v", r.Error)
	}
	var res mcp.ToolResult
	if err := json.Unmarshal(r.Result, &res); err != nil {
		t.Fatalf("decode tool result: %v", err)
	}
	return res
}

// inspectSession wires the standard init expectations shared by the
// inspection-tool tests.
func inspectSession(t *testing.T, m *calm.MockClient) {
	t.Helper()
	m.EXPECT().RegisterClient(mock.Anything, "claude-code").Return(true, nil).Once()
	m.EXPECT().CreateSession(mock.Anything, "claude-code", 60, mock.Anything).Return("tok-1", nil).Once()
	m.EXPECT().DeleteSession(mock.Anything, "tok-1").Return(nil).Once()
}

// eventCapture records the fire-and-forget WriteEvents payload for
// event-shape assertions.
func eventCapture(m *calm.MockClient) (<-chan struct{}, *[]calm.EventInput) {
	done := make(chan struct{})
	var events []calm.EventInput
	m.EXPECT().WriteEvents(mock.Anything, mock.Anything, mock.Anything).
		RunAndReturn(func(_ context.Context, _ string, ev []calm.EventInput) error {
			events = ev
			close(done)
			return nil
		}).Once()
	return done, &events
}

func TestReadFile_SummaryFusedLabel(t *testing.T) {
	m := calm.NewMockClient(t)
	inspectSession(t, m)
	m.EXPECT().Ingest(mock.Anything, "tok-1", mock.MatchedBy(func(in calm.IngestInput) bool {
		return in.Source == "calm:v1:file:read:big.txt"
	})).Return(calm.IngestSummary{Source: "calm:v1:file:read:big.txt", SectionsIndexed: 1, SectionsTotal: 1}, nil).Once()
	eventsDone, _ := eventCapture(m)

	ws := t.TempDir()
	writeFixture(t, ws, "big.txt", "content")
	h := newWorkspaceHarness(t, m, ws)
	initSession(t, h, "claude-code")

	res := callTool(t, h, 2, "calm_read_file", map[string]any{"path": "big.txt"})
	if res.IsError {
		t.Fatalf("read_file errored: %+v", res)
	}
	if got := resultText(t, res); !strings.Contains(got, "calm_search source=calm:v1:file:read:big.txt@") {
		t.Errorf("summary missing fused recall label; got:\n%s", got)
	}
	awaitSignal(t, eventsDone)
}

func TestReadFile_SmallInlineVerbatim(t *testing.T) {
	m := calm.NewMockClient(t)
	inspectSession(t, m)
	const content = "hello inline\n"
	m.EXPECT().Ingest(mock.Anything, "tok-1", mock.MatchedBy(func(in calm.IngestInput) bool {
		return in.Content == content
	})).Return(calm.IngestSummary{Source: "calm:v1:file:read:small.txt", SectionsIndexed: 1, SectionsTotal: 1}, nil).Once()
	eventsDone, _ := eventCapture(m)

	ws := t.TempDir()
	writeWorkspaceFileMCP(t, ws, "small.txt", content)
	h := newWorkspaceHarness(t, m, ws)
	initSession(t, h, "claude-code")

	res := callTool(t, h, 2, "calm_read_file", map[string]any{"path": "small.txt"})
	if got := resultText(t, res); got != content {
		t.Errorf("inline text = %q; want raw content verbatim", got)
	}
	awaitSignal(t, eventsDone)
}

// Capture-full-present-range: the ranged view shapes visible text only — CALM
// ingests the whole file, with the extension-keyed Format hint.
func TestReadFile_RangeCapturesFullPresentsSlice(t *testing.T) {
	var lines []string
	for i := 1; i <= 60; i++ {
		lines = append(lines, fmt.Sprintf(`{"line": %d, "pad": "xxxxxxxxxxxxxxxxxxxx"}`, i))
	}
	full := strings.Join(lines, "\n") + "\n"

	m := calm.NewMockClient(t)
	inspectSession(t, m)
	m.EXPECT().Ingest(mock.Anything, "tok-1", mock.MatchedBy(func(in calm.IngestInput) bool {
		return in.Content == full && in.Format == calm.FormatJSON
	})).Return(calm.IngestSummary{Source: "calm:v1:file:read:data.json", SectionsIndexed: 1, SectionsTotal: 1}, nil).Once()
	eventsDone, _ := eventCapture(m)

	ws := t.TempDir()
	writeWorkspaceFileMCP(t, ws, "data.json", full)
	h := newWorkspaceHarness(t, m, ws)
	initSession(t, h, "claude-code")

	res := callTool(t, h, 2, "calm_read_file", map[string]any{"path": "data.json", "start_line": 3, "end_line": 4})
	want := lines[2] + "\n" + lines[3] + "\n"
	if got := resultText(t, res); got != want {
		t.Errorf("ranged visible = %q; want lines 3-4 %q", got, want)
	}
	awaitSignal(t, eventsDone)
}

func TestReadFile_LocalFailuresNoCapture(t *testing.T) {
	m := calm.NewMockClient(t) // strict: no Ingest/WriteEvents expectations
	inspectSession(t, m)
	ws := t.TempDir()
	writeWorkspaceFileMCP(t, ws, "three.txt", "a\nb\nc\n")
	h := newWorkspaceHarness(t, m, ws)
	initSession(t, h, "claude-code")

	res := callTool(t, h, 2, "calm_read_file", map[string]any{"path": "missing.txt"})
	if !res.IsError || !strings.Contains(resultText(t, res), "read failed") {
		t.Errorf("missing file = %+v; want read-failed error result", res)
	}

	res = callTool(t, h, 3, "calm_read_file", map[string]any{"path": "three.txt", "start_line": 9})
	if !res.IsError || !strings.Contains(resultText(t, res), "past the end") {
		t.Errorf("past-EOF start = %+v; want past-the-end error result", res)
	}

	res = callTool(t, h, 4, "calm_read_file", map[string]any{"path": "three.txt", "start_line": 3, "end_line": 1})
	if !res.IsError || !strings.Contains(resultText(t, res), "invalid arguments") {
		t.Errorf("inverted range = %+v; want ArgError result", res)
	}
}

// An escaping path still reads and captures — under the program-equivalent
// coexist bucket, never a stable label (LABELING.md §4).
func TestReadFile_EscapePathCoexists(t *testing.T) {
	parent := t.TempDir()
	ws := filepath.Join(parent, "ws")
	if err := os.MkdirAll(ws, 0o750); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	writeWorkspaceFileMCP(t, parent, "esc.txt", "outside content\n")

	m := calm.NewMockClient(t)
	inspectSession(t, m)
	m.EXPECT().Ingest(mock.Anything, "tok-1", mock.MatchedBy(func(in calm.IngestInput) bool {
		return strings.HasPrefix(in.Source, "calm:v1:shell:cat#")
	})).Return(calm.IngestSummary{Source: "calm:v1:shell:cat#1", SectionsIndexed: 1, SectionsTotal: 1}, nil).Once()
	eventsDone, _ := eventCapture(m)

	h := newWorkspaceHarness(t, m, ws)
	initSession(t, h, "claude-code")

	res := callTool(t, h, 2, "calm_read_file", map[string]any{"path": "../esc.txt"})
	if res.IsError {
		t.Fatalf("escape read errored: %+v", res)
	}
	if got := resultText(t, res); got != "outside content\n" {
		t.Errorf("text = %q; want the outside content (labeling-only boundary)", got)
	}
	awaitSignal(t, eventsDone)
}

func TestListDir_LabelSortAndSuffix(t *testing.T) {
	m := calm.NewMockClient(t)
	inspectSession(t, m)
	m.EXPECT().Ingest(mock.Anything, "tok-1", mock.MatchedBy(func(in calm.IngestInput) bool {
		return in.Source == "calm:v1:file:list:sub" && in.Content == "a.txt\nnested/\nz.txt\n"
	})).Return(calm.IngestSummary{Source: "calm:v1:file:list:sub", SectionsIndexed: 1, SectionsTotal: 1}, nil).Once()
	eventsDone, _ := eventCapture(m)

	ws := t.TempDir()
	if err := os.MkdirAll(filepath.Join(ws, "sub", "nested"), 0o750); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	writeWorkspaceFileMCP(t, filepath.Join(ws, "sub"), "z.txt", "z")
	writeWorkspaceFileMCP(t, filepath.Join(ws, "sub"), "a.txt", "a")
	h := newWorkspaceHarness(t, m, ws)
	initSession(t, h, "claude-code")

	res := callTool(t, h, 2, "calm_list_dir", map[string]any{"path": "sub"})
	if got := resultText(t, res); got != "a.txt\nnested/\nz.txt\n" {
		t.Errorf("listing = %q; want sorted entries with dir suffix", got)
	}
	awaitSignal(t, eventsDone)
}

func TestListDir_MissingIsErrorNoCapture(t *testing.T) {
	m := calm.NewMockClient(t)
	inspectSession(t, m)
	h := newWorkspaceHarness(t, m, t.TempDir())
	initSession(t, h, "claude-code")

	res := callTool(t, h, 2, "calm_list_dir", map[string]any{"path": "nope"})
	if !res.IsError || !strings.Contains(resultText(t, res), "list failed") {
		t.Errorf("missing dir = %+v; want list-failed error result", res)
	}
}

// Flag variants refine matching, not identity: case-sensitive and
// case-insensitive runs write the same base label (LABELING.md §4).
func TestGrep_FlagVariantsShareBaseLabel(t *testing.T) {
	m := calm.NewMockClient(t)
	inspectSession(t, m)
	var mu sync.Mutex
	var sources []string
	m.EXPECT().Ingest(mock.Anything, "tok-1", mock.Anything).RunAndReturn(
		func(_ context.Context, _ string, in calm.IngestInput) (calm.IngestSummary, error) {
			mu.Lock()
			sources = append(sources, in.Source)
			mu.Unlock()
			return calm.IngestSummary{Source: in.Source, SectionsIndexed: 1, SectionsTotal: 1}, nil
		},
	).Times(2)
	var wg sync.WaitGroup
	wg.Add(2)
	m.EXPECT().WriteEvents(mock.Anything, mock.Anything, mock.Anything).
		RunAndReturn(func(context.Context, string, []calm.EventInput) error {
			wg.Done()
			return nil
		}).Times(2)

	ws := t.TempDir()
	writeWorkspaceFileMCP(t, ws, "code.go", "// TODO one\n// todo two\n")
	h := newWorkspaceHarness(t, m, ws)
	initSession(t, h, "claude-code")

	if res := callTool(t, h, 2, "calm_grep", map[string]any{"pattern": "TODO"}); res.IsError {
		t.Fatalf("grep errored: %+v", res)
	}
	if res := callTool(t, h, 3, "calm_grep", map[string]any{"pattern": "TODO", "case_insensitive": true}); res.IsError {
		t.Fatalf("case-insensitive grep errored: %+v", res)
	}
	wg.Wait()

	mu.Lock()
	defer mu.Unlock()
	if len(sources) != 2 || sources[0] != "calm:v1:search:grep:TODO" || sources[0] != sources[1] {
		t.Errorf("sources = %v; want both calm:v1:search:grep:TODO", sources)
	}
}

// Typed no-match semantics: exit 1 with empty output is a clean result — the
// label stays current via a sentinel capture and no error_observed fires.
func TestGrep_NoMatchCleanResult(t *testing.T) {
	m := calm.NewMockClient(t)
	inspectSession(t, m)
	m.EXPECT().Ingest(mock.Anything, "tok-1", mock.MatchedBy(func(in calm.IngestInput) bool {
		return in.Content == "(no matches)\n"
	})).Return(calm.IngestSummary{Source: "calm:v1:search:grep:zzznope", SectionsIndexed: 1, SectionsTotal: 1}, nil).Once()
	eventsDone, events := eventCapture(m)

	ws := t.TempDir()
	writeWorkspaceFileMCP(t, ws, "code.go", "nothing relevant\n")
	h := newWorkspaceHarness(t, m, ws)
	initSession(t, h, "claude-code")

	res := callTool(t, h, 2, "calm_grep", map[string]any{"pattern": "zzznope"})
	if res.IsError {
		t.Fatalf("no-match grep must not be an error result: %+v", res)
	}
	if got := resultText(t, res); got != "(no matches)\n" {
		t.Errorf("text = %q; want clean no-match payload", got)
	}
	awaitSignal(t, eventsDone)
	for _, e := range *events {
		if e.Type == "error_observed" {
			t.Errorf("no-match grep emitted error_observed: %+v", *events)
		}
	}
}

func TestGrep_BlankPatternArgError(t *testing.T) {
	m := calm.NewMockClient(t)
	inspectSession(t, m)
	h := newWorkspaceHarness(t, m, t.TempDir())
	initSession(t, h, "claude-code")

	res := callTool(t, h, 2, "calm_grep", map[string]any{"pattern": "  "})
	if !res.IsError || !strings.Contains(resultText(t, res), "invalid arguments") {
		t.Errorf("blank pattern = %+v; want ArgError result", res)
	}
}

func gitWorkspace(t *testing.T) string {
	t.Helper()
	if _, err := stdexec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	dir := t.TempDir()
	cmd := stdexec.Command("git", "init")
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git init: %v\n%s", err, out)
	}
	return dir
}

func TestGitStatus_DualLabelsAndGitEvent(t *testing.T) {
	m := calm.NewMockClient(t)
	inspectSession(t, m)
	var mu sync.Mutex
	var sources []string
	m.EXPECT().Ingest(mock.Anything, "tok-1", mock.Anything).RunAndReturn(
		func(_ context.Context, _ string, in calm.IngestInput) (calm.IngestSummary, error) {
			mu.Lock()
			sources = append(sources, in.Source)
			mu.Unlock()
			return calm.IngestSummary{Source: in.Source, SectionsIndexed: 1, SectionsTotal: 1}, nil
		},
	).Times(2)
	eventsDone, events := eventCapture(m)

	ws := gitWorkspace(t)
	writeWorkspaceFileMCP(t, ws, "untracked.txt", "u\n")
	h := newWorkspaceHarness(t, m, ws)
	initSession(t, h, "claude-code")

	if res := callTool(t, h, 2, "calm_git_status", map[string]any{}); res.IsError {
		t.Fatalf("git_status errored: %+v", res)
	}
	awaitSignal(t, eventsDone)

	mu.Lock()
	defer mu.Unlock()
	want := []string{"calm:v1:vcs:git:status#1", "calm:v1:vcs:git:status"}
	if len(sources) != 2 || sources[0] != want[0] || sources[1] != want[1] {
		t.Errorf("sources = %v; want history-then-latest %v", sources, want)
	}
	var sawGitOp bool
	for _, e := range *events {
		if e.Type == "git_operation" {
			sawGitOp = true
		}
		if e.Type == "tool_invocation" && e.Data["tool_name"] != "calm_git_status" {
			t.Errorf("tool_name = %v; want calm_git_status", e.Data["tool_name"])
		}
	}
	if !sawGitOp {
		t.Errorf("no git_operation event; got %+v", *events)
	}
}

func TestGitStatus_RejectsArguments(t *testing.T) {
	m := calm.NewMockClient(t)
	inspectSession(t, m)
	h := newWorkspaceHarness(t, m, t.TempDir())
	initSession(t, h, "claude-code")

	res := callTool(t, h, 2, "calm_git_status", map[string]any{"porcelain": true})
	if !res.IsError || !strings.Contains(resultText(t, res), "invalid arguments") {
		t.Errorf("unexpected args = %+v; want ArgError result", res)
	}
}

func TestGitDiff_RefGuardAndFailureCapture(t *testing.T) {
	m := calm.NewMockClient(t)
	inspectSession(t, m)
	var mu sync.Mutex
	var sources []string
	m.EXPECT().Ingest(mock.Anything, "tok-1", mock.Anything).RunAndReturn(
		func(_ context.Context, _ string, in calm.IngestInput) (calm.IngestSummary, error) {
			mu.Lock()
			sources = append(sources, in.Source)
			mu.Unlock()
			return calm.IngestSummary{Source: in.Source, SectionsIndexed: 1, SectionsTotal: 1}, nil
		},
	).Times(2)
	eventsDone, events := eventCapture(m)

	ws := gitWorkspace(t)
	h := newWorkspaceHarness(t, m, ws)
	initSession(t, h, "claude-code")

	// Flag-shaped ref rejects before any subprocess runs.
	res := callTool(t, h, 2, "calm_git_diff", map[string]any{"refs": []string{"-R"}})
	if !res.IsError || !strings.Contains(resultText(t, res), "invalid arguments") {
		t.Fatalf("flag-shaped ref = %+v; want ArgError result", res)
	}

	// A bad ref mirrors run_command: git's stderr is captured under the dual
	// labels and error_observed fires with the tool's own name.
	res = callTool(t, h, 3, "calm_git_diff", map[string]any{"refs": []string{"nosuchref"}})
	if res.IsError {
		t.Fatalf("bad-ref diff must not be an error result (local action ran): %+v", res)
	}
	awaitSignal(t, eventsDone)

	mu.Lock()
	defer mu.Unlock()
	want := []string{"calm:v1:vcs:git:diff:nosuchref#1", "calm:v1:vcs:git:diff:nosuchref"}
	if len(sources) != 2 || sources[0] != want[0] || sources[1] != want[1] {
		t.Errorf("sources = %v; want %v", sources, want)
	}
	var sawError bool
	for _, e := range *events {
		if e.Type == "error_observed" {
			sawError = true
			if e.Data["source"] != "calm_git_diff" {
				t.Errorf("error source = %v; want calm_git_diff", e.Data["source"])
			}
		}
	}
	if !sawError {
		t.Errorf("bad ref emitted no error_observed; got %+v", *events)
	}
}

// readOnlyHint is advertised on every read-only tool and absent on the
// mutating shell tool.
func TestToolsList_ReadOnlyAnnotations(t *testing.T) {
	h := newHarness(t, calm.NewMockClient(t))
	h.send(req(1, "tools/list", nil))
	r := h.recv()
	var list struct {
		Tools []struct {
			Name        string `json:"name"`
			Annotations *struct {
				ReadOnlyHint bool `json:"readOnlyHint"`
			} `json:"annotations"`
		} `json:"tools"`
	}
	if err := json.Unmarshal(r.Result, &list); err != nil {
		t.Fatalf("decode tools/list: %v", err)
	}
	readOnly := map[string]bool{
		"calm_search": true, "calm_read_file": true, "calm_list_dir": true,
		"calm_grep": true, "calm_git_status": true, "calm_git_diff": true,
	}
	for _, tool := range list.Tools {
		switch {
		case readOnly[tool.Name]:
			if tool.Annotations == nil || !tool.Annotations.ReadOnlyHint {
				t.Errorf("%s missing readOnlyHint annotation", tool.Name)
			}
		case tool.Name == "calm_run_command":
			if tool.Annotations != nil {
				t.Errorf("calm_run_command must not carry annotations; got %+v", tool.Annotations)
			}
		}
	}
}

func writeWorkspaceFileMCP(t *testing.T, dir, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o600); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
}
