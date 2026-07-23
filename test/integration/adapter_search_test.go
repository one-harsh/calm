// Copyright 2026 The CALM Authors
// SPDX-License-Identifier: Apache-2.0

package integration

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
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
// source label the first tool advertised — both the fused form the LLM sees and the
// base-only form programmatic callers might use.
func TestAdapterSearch_RunCommandThenSearchLoop(t *testing.T) {
	workspace := t.TempDir()
	const marker = "zphloxmarker"
	writeWorkspaceFile(t, workspace, "note.txt", marker+" lives in this file\n"+inlinePad)

	_, _, d := newAdapterLoop(t, workspace)

	rc := d.runCommand("cat note.txt")
	if rc.IsError {
		t.Fatalf("run_command errored: %+v", rc)
	}
	fused := parseSearchSourceFused(t, rc.Content[0].Text)
	base := parseSearchSource(t, rc.Content[0].Text)

	// Fused form: LLM-facing default. Adapter strips the @<token> before
	// forwarding to CALM.
	res := d.search([]string{marker}, fused)
	if res.IsError {
		t.Fatalf("search with fused source errored: %+v", res)
	}
	if !strings.Contains(res.Content[0].Text, marker) {
		t.Fatalf("search with fused source did not return marker; got:\n%s", res.Content[0].Text)
	}

	// Base-only form: LABELING.md-sanctioned bypass for programmatic clients.
	// Forwards unchanged to CALM.
	res = d.search([]string{marker}, base)
	if res.IsError {
		t.Fatalf("search with base source errored: %+v", res)
	}
	if !strings.Contains(res.Content[0].Text, marker) {
		t.Fatalf("search with base source did not return marker; got:\n%s", res.Content[0].Text)
	}
}

// A fused source label from an earlier invocation whose token has been
// replaced by a later capture resolves locally to session_lost — the adapter
// rejects before contacting CALM, so the agent sees a clear staleness signal
// rather than empty search results from the current session.
func TestAdapterSearch_StaleFusedLabel_SessionLost(t *testing.T) {
	workspace := t.TempDir()
	writeWorkspaceFile(t, workspace, "note.txt", "first capture\n"+inlinePad)

	_, _, d := newAdapterLoop(t, workspace)

	// First capture — grab its fused recall label.
	rc1 := d.runCommand("cat note.txt")
	if rc1.IsError {
		t.Fatalf("first run_command errored: %+v", rc1)
	}
	staleFused := parseSearchSourceFused(t, rc1.Content[0].Text)

	// Second capture under the same source — replaces the first token in
	// the adapter's registry (replace-mode source).
	writeWorkspaceFile(t, workspace, "note.txt", "second capture\n")
	rc2 := d.runCommand("cat note.txt")
	if rc2.IsError {
		t.Fatalf("second run_command errored: %+v", rc2)
	}

	// Search with the FIRST fused label — its token no longer validates.
	res := d.search([]string{"capture"}, staleFused)
	if !res.IsError {
		t.Fatalf("stale fused label must be an error result: %+v", res)
	}
	if !strings.Contains(res.Content[0].Text, "session_lost") {
		t.Fatalf("expected session_lost phrasing; got:\n%s", res.Content[0].Text)
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

// searchDocumentOrder drives the queries-less, source-scoped shape of
// calm_search — document-order reread — optionally continuing from an offset
// and overriding the response byte budget (0 rides the server default).
func (d *mcpDriver) searchDocumentOrder(source string, offset, budgetBytes int) mcp.ToolResult {
	d.t.Helper()
	args := map[string]any{"source": source}
	if offset > 0 {
		args["offset"] = offset
	}
	if budgetBytes > 0 {
		args["budget_bytes"] = budgetBytes
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

var docOrderContinuationPattern = regexp.MustCompile(`more chunks remain — call calm_search again with source and offset: (\d+)`)

func parseContinuationOffset(text string) (int, bool) {
	m := docOrderContinuationPattern.FindStringSubmatch(text)
	if len(m) != 2 {
		return 0, false
	}
	n, err := strconv.Atoi(m[1])
	return n, err == nil
}

const docOrderTruncatedMarker = "[truncated — raise budget_bytes or use a ranked query for the rest]"

// docOrderTruncatedPrefix extracts the first chunk's rendered snippet from a
// document-order page that carries the truncated marker — the exact-text
// prefix the recovery re-request must return whole.
func docOrderTruncatedPrefix(t *testing.T, text string) string {
	t.Helper()
	body, _, found := strings.Cut(text, "\n"+docOrderTruncatedMarker)
	if !found {
		t.Fatalf("page carries no truncated marker:\n%s", text)
	}
	title := strings.Index(body, "\n## ")
	if title < 0 {
		t.Fatalf("page has no chunk title line:\n%s", text)
	}
	rest := body[title+1:]
	nl := strings.Index(rest, "\n")
	if nl < 0 {
		t.Fatalf("chunk title line unterminated:\n%s", text)
	}
	return rest[nl+1:]
}

// TestAdapterSearch_DocumentOrderReread walks a large captured output in original
// document order: a source scope with no queries returns the source's chunks in
// order, paginated by offset until the continuation hint disappears, spanning the
// whole capture — while a ranked query over the same source still works, so both
// retrieval modes coexist on one primitive.
func TestAdapterSearch_DocumentOrderReread(t *testing.T) {
	workspace := t.TempDir()
	var content strings.Builder
	const lines = 600
	for i := 1; i <= lines; i++ {
		fmt.Fprintf(&content, "docline %03d payload alpha beta gamma delta epsilon\n", i)
	}
	// A distinctive token for the ranked-mode coexistence check.
	const rankedMarker = "zphloxmiddle"
	writeWorkspaceFile(t, workspace, "big.txt", content.String()+rankedMarker+" sits near the end\n")

	_, _, d := newAdapterLoop(t, workspace)

	rc := d.runCommand("cat big.txt")
	if rc.IsError {
		t.Fatalf("run_command errored: %+v", rc)
	}
	source := parseSearchSourceFused(t, rc.Content[0].Text)

	// Document-order walk: follow the named offset until the hint is gone.
	var seen strings.Builder
	offset, pages := 0, 0
	for {
		res := d.searchDocumentOrder(source, offset, 0)
		if res.IsError {
			t.Fatalf("document-order search errored at offset %d: %+v", offset, res)
		}
		text := res.Content[0].Text
		seen.WriteString(text)
		pages++
		for _, banned := range []string{"[document]", "[primary]", "[trigram]"} {
			if strings.Contains(text, banned) {
				t.Fatalf("document-order output carried the %q ranking annotation:\n%s", banned, text)
			}
		}
		next, ok := parseContinuationOffset(text)
		if !ok {
			break
		}
		if next <= offset {
			t.Fatalf("continuation offset did not advance: was %d, next %d", offset, next)
		}
		offset = next
		if pages > 100 {
			t.Fatal("document-order walk did not terminate")
		}
	}
	if pages < 2 {
		t.Fatalf("expected the capture to span multiple document-order pages; got %d", pages)
	}
	walked := seen.String()
	for _, want := range []string{"docline 001", "docline 600"} {
		if !strings.Contains(walked, want) {
			t.Errorf("document-order walk did not span the capture; missing %q", want)
		}
	}

	// Offset past the end is a healthy empty page, not a degradation shape.
	end := d.searchDocumentOrder(source, offset+10_000, 0)
	if end.IsError {
		t.Fatalf("offset-past-end must not be an error: %+v", end)
	}
	if !strings.Contains(end.Content[0].Text, "no chunks at this offset") {
		t.Errorf("offset-past-end should read as an empty page; got:\n%s", end.Content[0].Text)
	}

	// The truncated marker's advertised recovery works end-to-end: a budget
	// the first chunk alone exceeds yields an exact-text prefix with the
	// marker; re-requesting the SAME offset with a larger budget_bytes
	// returns the chunk whole, marker gone.
	small := d.searchDocumentOrder(source, 0, 512)
	if small.IsError {
		t.Fatalf("small-budget document-order search errored: %+v", small)
	}
	smallText := small.Content[0].Text
	if !strings.Contains(smallText, docOrderTruncatedMarker) {
		t.Fatalf("expected the truncated marker at a 512-byte budget (chunks target 2048 bytes); got:\n%s", smallText)
	}
	prefix := docOrderTruncatedPrefix(t, smallText)
	if prefix == "" {
		t.Fatal("truncated page rendered an empty prefix")
	}
	recovered := d.searchDocumentOrder(source, 0, 16384)
	if recovered.IsError {
		t.Fatalf("larger-budget re-request errored: %+v", recovered)
	}
	recoveredText := recovered.Content[0].Text
	if strings.Contains(recoveredText, docOrderTruncatedMarker) {
		t.Fatalf("larger budget must return the chunk whole, no marker; got:\n%s", recoveredText)
	}
	if !strings.Contains(recoveredText, prefix) {
		t.Errorf("recovered page does not contain the truncated prefix — not the same chunk whole;\nprefix:\n%s\npage:\n%s", prefix, recoveredText)
	}

	// Both modes coexist: a ranked query over the same source still works.
	ranked := d.search([]string{rankedMarker}, source)
	if ranked.IsError {
		t.Fatalf("ranked search over the same source errored: %+v", ranked)
	}
	if !strings.Contains(ranked.Content[0].Text, rankedMarker) {
		t.Errorf("ranked search did not return the marker; got:\n%s", ranked.Content[0].Text)
	}
}

// TestAdapterSearch_CalmDown_UnreachablePhrasing proves search reports the
// canonical calm_unreachable degradation phrasing (not empty results, not a
// bare error string) when CALM is unreachable — search is the operation, so
// there is no raw fallback.
func TestAdapterSearch_CalmDown_UnreachablePhrasing(t *testing.T) {
	inner, err := calm.NewGenapiClient("http://127.0.0.1:1", "", nil)
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
