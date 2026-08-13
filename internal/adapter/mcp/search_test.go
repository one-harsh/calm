// Copyright 2026 The CALM Authors
// SPDX-License-Identifier: Apache-2.0

package mcp_test

import (
	"context"
	"encoding/json"
	"errors"
	"regexp"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/mock"

	"github.com/one-harsh/calm/internal/adapter/calm"
	"github.com/one-harsh/calm/internal/adapter/mcp"
	"github.com/one-harsh/calm/internal/adapter/obs"
)

// recallLabelPattern captures the source label — including any fused
// @<token> suffix — from a compact rep's recall hint line.
var recallLabelPattern = regexp.MustCompile(`calm_search source=(\S+)`)

func extractRecallLabel(t *testing.T, text string) string {
	t.Helper()
	m := recallLabelPattern.FindStringSubmatch(text)
	if len(m) != 2 {
		t.Fatalf("no recall hint found in compact rep; text was:\n%s", text)
	}
	return m[1]
}

func callSearch(t *testing.T, h *harness, id int, args map[string]any) mcp.ToolResult {
	t.Helper()
	h.send(req(id, "tools/call", map[string]any{"name": "calm_search", "arguments": args}))
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

func oneHit(query string) calm.SearchResults {
	return calm.SearchResults{Queries: []calm.QueryResult{{
		Query: query,
		Hits: []calm.Hit{{
			Title: "note.txt", Snippet: "zphlox lives here",
			Source: "calm:v1:file:read:note.txt", MatchLayer: "primary",
		}},
	}}}
}

func TestSearch_ReturnsRankedSnippets(t *testing.T) {
	m := calm.NewMockClient(t)
	m.EXPECT().RegisterClient(mock.Anything, "claude-code").Return(true, nil).Once()
	m.EXPECT().CreateSession(mock.Anything, "claude-code", 60, mock.Anything).Return("tok-1", nil).Once()
	m.EXPECT().DeleteSession(mock.Anything, "tok-1").Return(nil).Once()
	m.EXPECT().Search(mock.Anything, "tok-1", mock.MatchedBy(func(in calm.SearchInput) bool {
		return len(in.Queries) == 1 && in.Queries[0] == "zphlox" && in.Source == "" && in.Limit == 0 && in.BudgetBytes == 0
	})).Return(oneHit("zphlox"), nil).Once()

	h := newHarness(t, m)
	initSession(t, h, "claude-code")

	res := callSearch(t, h, 2, map[string]any{"queries": []string{"zphlox"}})
	if res.IsError {
		t.Fatalf("unexpected isError: %+v", res)
	}
	text := resultText(t, res)
	for _, want := range []string{"zphlox lives here", "calm:v1:file:read:note.txt", "[primary]"} {
		if !strings.Contains(text, want) {
			t.Errorf("result missing %q; got:\n%s", want, text)
		}
	}
}

func TestSearch_ScopesToSource(t *testing.T) {
	const src = "calm:v1:file:read:note.txt"
	m := calm.NewMockClient(t)
	m.EXPECT().RegisterClient(mock.Anything, "claude-code").Return(true, nil).Once()
	m.EXPECT().CreateSession(mock.Anything, "claude-code", 60, mock.Anything).Return("tok-1", nil).Once()
	m.EXPECT().DeleteSession(mock.Anything, "tok-1").Return(nil).Once()
	m.EXPECT().Search(mock.Anything, "tok-1", mock.MatchedBy(func(in calm.SearchInput) bool {
		return in.Source == src
	})).Return(oneHit("zphlox"), nil).Once()

	h := newHarness(t, m)
	initSession(t, h, "claude-code")

	if res := callSearch(t, h, 2, map[string]any{"queries": []string{"zphlox"}, "source": src}); res.IsError {
		t.Fatalf("unexpected isError: %+v", res)
	}
}

func docOrderResults(next *int, hits ...calm.Hit) calm.SearchResults {
	return calm.SearchResults{
		Queries:    []calm.QueryResult{{Query: "", Hits: hits}},
		NextOffset: next,
	}
}

// A source scope with no queries selects document-order mode: CALM is called
// with empty queries, and the output is a plain sequential reread — no ranking
// annotations.
func TestSearch_SourceOnly_RoutesDocumentOrder(t *testing.T) {
	const src = "calm:v1:file:read:big.txt"
	m := calm.NewMockClient(t)
	m.EXPECT().RegisterClient(mock.Anything, "claude-code").Return(true, nil).Once()
	m.EXPECT().CreateSession(mock.Anything, "claude-code", 60, mock.Anything).Return("tok-1", nil).Once()
	m.EXPECT().DeleteSession(mock.Anything, "tok-1").Return(nil).Once()
	m.EXPECT().Search(mock.Anything, "tok-1", mock.MatchedBy(func(in calm.SearchInput) bool {
		return len(in.Queries) == 0 && in.Source == src && in.Offset == 0 && in.BudgetBytes == calm.DocumentOrderBudgetDefault
	})).Return(docOrderResults(
		nil,
		calm.Hit{Title: "big.txt#1", Snippet: "first chunk body", Source: src, MatchLayer: "document"},
		calm.Hit{Title: "big.txt#2", Snippet: "second chunk body", Source: src, MatchLayer: "document"},
	), nil).Once()

	h := newHarness(t, m)
	initSession(t, h, "claude-code")

	res := callSearch(t, h, 2, map[string]any{"source": src})
	if res.IsError {
		t.Fatalf("document-order search must not error: %+v", res)
	}
	text := resultText(t, res)
	for _, want := range []string{"document order", "big.txt#1", "first chunk body", "second chunk body"} {
		if !strings.Contains(text, want) {
			t.Errorf("missing %q in doc-order output:\n%s", want, text)
		}
	}
	for _, banned := range []string{"[document]", "[primary]", "[trigram]"} {
		if strings.Contains(text, banned) {
			t.Errorf("doc-order output must not carry the %q ranking annotation:\n%s", banned, text)
		}
	}
}

// Document-order continuation: the requested offset reaches CALM, and a present
// next_offset renders the literal continuation hint naming the next call shape.
func TestSearch_DocumentOrder_ForwardsOffsetAndHintsContinuation(t *testing.T) {
	const src = "calm:v1:file:read:big.txt"
	next := 4
	m := calm.NewMockClient(t)
	m.EXPECT().RegisterClient(mock.Anything, "claude-code").Return(true, nil).Once()
	m.EXPECT().CreateSession(mock.Anything, "claude-code", 60, mock.Anything).Return("tok-1", nil).Once()
	m.EXPECT().DeleteSession(mock.Anything, "tok-1").Return(nil).Once()
	m.EXPECT().Search(mock.Anything, "tok-1", mock.MatchedBy(func(in calm.SearchInput) bool {
		return len(in.Queries) == 0 && in.Source == src && in.Offset == 2
	})).Return(docOrderResults(
		&next,
		calm.Hit{Title: "big.txt#3", Snippet: "third chunk body", Source: src, MatchLayer: "document"},
	), nil).Once()

	h := newHarness(t, m)
	initSession(t, h, "claude-code")

	res := callSearch(t, h, 2, map[string]any{"source": src, "offset": 2})
	if res.IsError {
		t.Fatalf("document-order search must not error: %+v", res)
	}
	if got := resultText(t, res); !strings.Contains(got, "offset: 4") {
		t.Errorf("expected continuation hint naming next offset 4; got:\n%s", got)
	}
}

// An offset-past-end page is a healthy empty result, distinct from a
// degradation shape — the agent can tell "nothing here" from "CALM is down".
func TestSearch_DocumentOrder_EmptyPageIsNotError(t *testing.T) {
	const src = "calm:v1:file:read:big.txt"
	m := calm.NewMockClient(t)
	m.EXPECT().RegisterClient(mock.Anything, "claude-code").Return(true, nil).Once()
	m.EXPECT().CreateSession(mock.Anything, "claude-code", 60, mock.Anything).Return("tok-1", nil).Once()
	m.EXPECT().DeleteSession(mock.Anything, "tok-1").Return(nil).Once()
	m.EXPECT().Search(mock.Anything, "tok-1", mock.MatchedBy(func(in calm.SearchInput) bool {
		return len(in.Queries) == 0 && in.Offset == 999
	})).Return(calm.SearchResults{}, nil).Once()

	h := newHarness(t, m)
	initSession(t, h, "claude-code")

	res := callSearch(t, h, 2, map[string]any{"source": src, "offset": 999})
	if res.IsError {
		t.Fatalf("offset-past-end page must not be an error: %+v", res)
	}
	if got := resultText(t, res); !strings.Contains(got, "no chunks at this offset") {
		t.Errorf("text = %q; want 'no chunks at this offset'", got)
	}
}

// Ranked mode ignores offset — it is never forwarded to CALM even when the
// agent supplies it alongside queries.
func TestSearch_RankedIgnoresOffset(t *testing.T) {
	const src = "calm:v1:file:read:note.txt"
	m := calm.NewMockClient(t)
	m.EXPECT().RegisterClient(mock.Anything, "claude-code").Return(true, nil).Once()
	m.EXPECT().CreateSession(mock.Anything, "claude-code", 60, mock.Anything).Return("tok-1", nil).Once()
	m.EXPECT().DeleteSession(mock.Anything, "tok-1").Return(nil).Once()
	m.EXPECT().Search(mock.Anything, "tok-1", mock.MatchedBy(func(in calm.SearchInput) bool {
		return len(in.Queries) == 1 && in.Source == src && in.Offset == 0
	})).Return(oneHit("zphlox"), nil).Once()

	h := newHarness(t, m)
	initSession(t, h, "claude-code")

	if res := callSearch(t, h, 2, map[string]any{"queries": []string{"zphlox"}, "source": src, "offset": 5}); res.IsError {
		t.Fatalf("ranked search with a stray offset must not error: %+v", res)
	}
}

// budget_bytes is a wire parameter in both modes — forwarded when the agent
// sets it, whether the call is ranked or a document-order reread. (The
// omitted-when-unset side is pinned by the BudgetBytes == 0 matchers above.)
func TestSearch_ForwardsBudgetBytesBothModes(t *testing.T) {
	const src = "calm:v1:file:read:big.txt"
	m := calm.NewMockClient(t)
	m.EXPECT().RegisterClient(mock.Anything, "claude-code").Return(true, nil).Once()
	m.EXPECT().CreateSession(mock.Anything, "claude-code", 60, mock.Anything).Return("tok-1", nil).Once()
	m.EXPECT().DeleteSession(mock.Anything, "tok-1").Return(nil).Once()
	m.EXPECT().Search(mock.Anything, "tok-1", mock.MatchedBy(func(in calm.SearchInput) bool {
		return len(in.Queries) == 1 && in.BudgetBytes == 2048
	})).Return(oneHit("zphlox"), nil).Once()
	m.EXPECT().Search(mock.Anything, "tok-1", mock.MatchedBy(func(in calm.SearchInput) bool {
		return len(in.Queries) == 0 && in.Source == src && in.Offset == 3 && in.BudgetBytes == 512
	})).Return(docOrderResults(
		nil,
		calm.Hit{Title: "big.txt#4", Snippet: "fourth chunk body", Source: src, MatchLayer: "document"},
	), nil).Once()

	h := newHarness(t, m)
	initSession(t, h, "claude-code")

	if res := callSearch(t, h, 2, map[string]any{"queries": []string{"zphlox"}, "budget_bytes": 2048}); res.IsError {
		t.Fatalf("ranked search with budget_bytes must not error: %+v", res)
	}
	if res := callSearch(t, h, 3, map[string]any{"source": src, "offset": 3, "budget_bytes": 512}); res.IsError {
		t.Fatalf("document-order search with budget_bytes must not error: %+v", res)
	}
}

func TestSearch_PassesLimit(t *testing.T) {
	m := calm.NewMockClient(t)
	m.EXPECT().RegisterClient(mock.Anything, "claude-code").Return(true, nil).Once()
	m.EXPECT().CreateSession(mock.Anything, "claude-code", 60, mock.Anything).Return("tok-1", nil).Once()
	m.EXPECT().DeleteSession(mock.Anything, "tok-1").Return(nil).Once()
	m.EXPECT().Search(mock.Anything, "tok-1", mock.MatchedBy(func(in calm.SearchInput) bool {
		return in.Limit == 5
	})).Return(oneHit("zphlox"), nil).Once()

	h := newHarness(t, m)
	initSession(t, h, "claude-code")

	if res := callSearch(t, h, 2, map[string]any{"queries": []string{"zphlox"}, "limit": 5}); res.IsError {
		t.Fatalf("unexpected isError: %+v", res)
	}
}

func TestSearch_GroupsMultipleQueries(t *testing.T) {
	m := calm.NewMockClient(t)
	m.EXPECT().RegisterClient(mock.Anything, "claude-code").Return(true, nil).Once()
	m.EXPECT().CreateSession(mock.Anything, "claude-code", 60, mock.Anything).Return("tok-1", nil).Once()
	m.EXPECT().DeleteSession(mock.Anything, "tok-1").Return(nil).Once()
	m.EXPECT().Search(mock.Anything, "tok-1", mock.Anything).Return(calm.SearchResults{Queries: []calm.QueryResult{
		{Query: "alpha", Hits: []calm.Hit{{Title: "a", Snippet: "a snip", Source: "sa", MatchLayer: "primary"}}},
		{Query: "beta", Hits: []calm.Hit{{Title: "b", Snippet: "b snip", Source: "sb", MatchLayer: "trigram"}}},
	}}, nil).Once()

	h := newHarness(t, m)
	initSession(t, h, "claude-code")

	text := resultText(t, callSearch(t, h, 2, map[string]any{"queries": []string{"alpha", "beta"}}))
	if !strings.Contains(text, `# "alpha"`) || !strings.Contains(text, `# "beta"`) {
		t.Errorf("expected both query groups; got:\n%s", text)
	}
}

func TestSearch_EmptyResultsIsNotError(t *testing.T) {
	m := calm.NewMockClient(t)
	m.EXPECT().RegisterClient(mock.Anything, "claude-code").Return(true, nil).Once()
	m.EXPECT().CreateSession(mock.Anything, "claude-code", 60, mock.Anything).Return("tok-1", nil).Once()
	m.EXPECT().DeleteSession(mock.Anything, "tok-1").Return(nil).Once()
	m.EXPECT().Search(mock.Anything, "tok-1", mock.Anything).Return(calm.SearchResults{}, nil).Once()

	h := newHarness(t, m)
	initSession(t, h, "claude-code")

	res := callSearch(t, h, 2, map[string]any{"queries": []string{"nope"}})
	if res.IsError {
		t.Fatalf("empty results must not be an error: %+v", res)
	}
	if got := resultText(t, res); !strings.Contains(got, "no matches") {
		t.Errorf("text = %q; want 'no matches'", got)
	}
}

func TestSearch_CalmDown_UnreachablePhrasing(t *testing.T) {
	// No initialize → no session token. Zero mock expectations, so any Search call fails.
	m := calm.NewMockClient(t)
	h := newHarness(t, m)

	res := callSearch(t, h, 1, map[string]any{"queries": []string{"x"}})
	if !res.IsError {
		t.Fatalf("CALM-down must be an error result: %+v", res)
	}
	want := obs.DegradedPhrase(obs.DegradedReasonCalmUnreachable)
	if got := resultText(t, res); got != want {
		t.Errorf("text = %q; want canonical calm_unreachable phrasing: %q", got, want)
	}
}

func TestSearch_SearchError_UnreachablePhrasingThenStderr(t *testing.T) {
	m := calm.NewMockClient(t)
	m.EXPECT().RegisterClient(mock.Anything, "claude-code").Return(true, nil).Once()
	m.EXPECT().CreateSession(mock.Anything, "claude-code", 60, mock.Anything).Return("tok-1", nil).Once()
	m.EXPECT().DeleteSession(mock.Anything, "tok-1").Return(nil).Once()
	m.EXPECT().Search(mock.Anything, "tok-1", mock.Anything).
		Return(calm.SearchResults{}, errors.New("boom")).Once()

	h := newHarness(t, m)
	initSession(t, h, "claude-code")

	res := callSearch(t, h, 2, map[string]any{"queries": []string{"x"}})
	if !res.IsError {
		t.Fatalf("search error must be an error result: %+v", res)
	}
	want := obs.DegradedPhrase(obs.DegradedReasonCalmUnreachable) + "\n\n[stderr]\nboom"
	if got := resultText(t, res); got != want {
		t.Errorf("text = %q; want canonical phrasing then [stderr] block: %q", got, want)
	}
}

// Neither queries nor a source selects a mode — reject locally. (Empty
// queries with no source is the degenerate form of this.)
func TestSearch_NeitherQueriesNorSourceIsArgError(t *testing.T) {
	m := calm.NewMockClient(t)
	h := newHarness(t, m)

	res := callSearch(t, h, 1, map[string]any{"queries": []string{}})
	if !res.IsError {
		t.Fatalf("empty queries with no source must be an error result: %+v", res)
	}
	if got := resultText(t, res); !strings.Contains(got, "queries or source is required") {
		t.Errorf("text = %q; want 'queries or source is required'", got)
	}

	res = callSearch(t, h, 2, map[string]any{})
	if got := resultText(t, res); !res.IsError || !strings.Contains(got, "queries or source is required") {
		t.Errorf("bare call: text = %q, isError = %v; want 'queries or source is required'", got, res.IsError)
	}
}

// A whitespace-only query violates CALM's SearchRequest schema (minLength 1
// per query) — reject locally as an arg error rather than forwarding and
// having CALM 400 come back as calm_unreachable.
func TestSearch_BlankQueryIsArgError(t *testing.T) {
	m := calm.NewMockClient(t)
	h := newHarness(t, m)

	res := callSearch(t, h, 1, map[string]any{"queries": []string{"  "}})
	if !res.IsError {
		t.Fatalf("blank query must be an error result: %+v", res)
	}
	got := resultText(t, res)
	if !strings.Contains(got, "invalid arguments") || !strings.Contains(got, "queries[0] is blank") {
		t.Errorf("text = %q; want invalid-arguments error mentioning the blank query index", got)
	}
}

// Mixed array (some non-blank, some blank) must also fail locally — the
// previous "any non-blank" check would have passed this to CALM, where the
// 400 would surface as calm_unreachable.
func TestSearch_MixedBlankQueriesIsArgError(t *testing.T) {
	m := calm.NewMockClient(t)
	h := newHarness(t, m)

	res := callSearch(t, h, 1, map[string]any{"queries": []string{"real query", ""}})
	if !res.IsError {
		t.Fatalf("mixed blank queries must be an error result: %+v", res)
	}
	got := resultText(t, res)
	if !strings.Contains(got, "invalid arguments") || !strings.Contains(got, "queries[1] is blank") {
		t.Errorf("text = %q; want invalid-arguments error pinpointing the blank index", got)
	}
	if strings.Contains(got, "calm_unreachable") {
		t.Errorf("text incorrectly classified as calm_unreachable: %q", got)
	}
}

// Base-only source labels forward to CALM unchanged — the LABELING.md-
// sanctioned bypass for shell-substrate references and programmatic clients.
// (Regression guard: covered inline in TestSearch_ScopesToSource above.)

// A fused source label with a registered token gets its @token stripped
// before forwarding — CALM's grammar doesn't parse @token, and the token is
// pure adapter-side staleness machinery.
func TestSearch_FusedValidTokenStripsBeforeForward(t *testing.T) {
	m := calm.NewMockClient(t)
	m.EXPECT().RegisterClient(mock.Anything, "claude-code").Return(true, nil).Once()
	m.EXPECT().CreateSession(mock.Anything, "claude-code", 60, mock.Anything).Return("tok-1", nil).Once()
	m.EXPECT().DeleteSession(mock.Anything, "tok-1").Return(nil).Once()
	m.EXPECT().Ingest(mock.Anything, "tok-1", mock.Anything).
		Return(calm.IngestSummary{Source: "calm:v1:file:read:foo.txt", SectionsIndexed: 1, SectionsTotal: 1}, nil).Once()
	eventsDone := writeEventsSignal(m, nil)
	// CALM must receive the base label — @<token> stripped.
	m.EXPECT().Search(mock.Anything, "tok-1", mock.MatchedBy(func(in calm.SearchInput) bool {
		return in.Source == "calm:v1:file:read:foo.txt"
	})).Return(calm.SearchResults{}, nil).Once()

	ws := t.TempDir()
	writeFixture(t, ws, "foo.txt", "x")
	h := newWorkspaceHarness(t, m, ws)
	initSession(t, h, "claude-code")

	// Register a token via a normal capture (fixture is above the inline
	// threshold, so the recall label is advertised).
	res1 := callRunCommand(t, h, 2, map[string]any{"command": "cat foo.txt"})
	fusedLabel := extractRecallLabel(t, resultText(t, res1))
	if !strings.Contains(fusedLabel, "@") {
		t.Fatalf("expected fused recall label with @<token>; got %q", fusedLabel)
	}

	// Search with the fused form — mock asserts base was forwarded.
	_ = callSearch(t, h, 3, map[string]any{"queries": []string{"x"}, "source": fusedLabel})
	awaitSignal(t, eventsDone)
}

// A fused source label whose token has been replaced by a later invocation
// (or never registered at all) resolves locally to session_lost — CALM is
// never contacted.
func TestSearch_StaleFusedTokenIsSessionLost(t *testing.T) {
	m := calm.NewMockClient(t)
	m.EXPECT().RegisterClient(mock.Anything, "claude-code").Return(true, nil).Once()
	m.EXPECT().CreateSession(mock.Anything, "claude-code", 60, mock.Anything).Return("tok-1", nil).Once()
	m.EXPECT().DeleteSession(mock.Anything, "tok-1").Return(nil).Once()
	// Two captures under the same source — the second's token replaces the
	// first's. calm.Search must NOT be called; local validation rejects.
	m.EXPECT().Ingest(mock.Anything, "tok-1", mock.Anything).
		Return(calm.IngestSummary{Source: "calm:v1:file:read:hello.txt", SectionsIndexed: 1, SectionsTotal: 1}, nil).Times(2)
	// Fire-and-forget: two run_commands emit two event batches.
	var eventWG sync.WaitGroup
	eventWG.Add(2)
	m.EXPECT().WriteEvents(mock.Anything, mock.Anything, mock.Anything).
		RunAndReturn(func(context.Context, string, []calm.EventInput) error {
			eventWG.Done()
			return nil
		}).Times(2)

	ws := t.TempDir()
	writeFixture(t, ws, "hello.txt", "x")
	h := newWorkspaceHarness(t, m, ws)
	initSession(t, h, "claude-code")

	// First capture — grab its fused recall label.
	res1 := callRunCommand(t, h, 2, map[string]any{"command": "cat hello.txt"})
	staleLabel := extractRecallLabel(t, resultText(t, res1))

	// Second capture under the same source — replaces the token.
	_ = callRunCommand(t, h, 3, map[string]any{"command": "cat hello.txt"})

	// Search with the FIRST (now stale) label — must session_lost locally.
	res := callSearch(t, h, 4, map[string]any{"queries": []string{"x"}, "source": staleLabel})
	if !res.IsError {
		t.Fatalf("stale fused label must be an error result: %+v", res)
	}
	got := resultText(t, res)
	if !strings.Contains(got, "session_lost") {
		t.Errorf("text missing session_lost phrasing; got: %s", got)
	}
	eventWG.Wait()
}

func TestSearch_InvalidArgumentsIsError(t *testing.T) {
	m := calm.NewMockClient(t)
	h := newHarness(t, m)

	// arguments as a JSON string (not an object) fails to unmarshal into the schema.
	h.send(req(1, "tools/call", map[string]any{"name": "calm_search", "arguments": "not-an-object"}))
	r := h.recv()
	if r.Error != nil {
		t.Fatalf("want isError result, got protocol error: %+v", r.Error)
	}
	var res mcp.ToolResult
	if err := json.Unmarshal(r.Result, &res); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !res.IsError || !strings.Contains(resultText(t, res), "invalid arguments") {
		t.Fatalf("want invalid-arguments error result; got %+v", res)
	}
}

// Adapter-side bounds match CALM's SearchRequest schema (maxItems 10) — too
// many queries surface as an argument error, NOT as calm_unreachable. The
// agent gave bad input; CALM was never involved.
func TestSearch_TooManyQueriesIsArgError(t *testing.T) {
	m := calm.NewMockClient(t)
	h := newHarness(t, m)

	queries := make([]string, 11)
	for i := range queries {
		queries[i] = "q"
	}
	res := callSearch(t, h, 1, map[string]any{"queries": queries})
	if !res.IsError {
		t.Fatalf("too many queries must be an error result: %+v", res)
	}
	got := resultText(t, res)
	if !strings.Contains(got, "invalid arguments") || !strings.Contains(got, "too many queries") {
		t.Errorf("text = %q; want invalid-arguments error mentioning too many queries", got)
	}
	if strings.Contains(got, "calm_unreachable") {
		t.Errorf("text incorrectly classified as calm_unreachable: %q", got)
	}
}

// Out-of-range limit (above CALM's max of 50) surfaces as an argument error.
func TestSearch_LimitOutOfRangeIsArgError(t *testing.T) {
	m := calm.NewMockClient(t)
	h := newHarness(t, m)

	res := callSearch(t, h, 1, map[string]any{"queries": []string{"x"}, "limit": 999})
	if !res.IsError {
		t.Fatalf("out-of-range limit must be an error result: %+v", res)
	}
	got := resultText(t, res)
	if !strings.Contains(got, "invalid arguments") || !strings.Contains(got, "limit out of range") {
		t.Errorf("text = %q; want invalid-arguments error mentioning limit out of range", got)
	}
	if strings.Contains(got, "calm_unreachable") {
		t.Errorf("text incorrectly classified as calm_unreachable: %q", got)
	}
}

func TestSearch_NoMatchesNotesSourceScope(t *testing.T) {
	const src = "calm:v1:file:read:note.txt"
	m := calm.NewMockClient(t)
	m.EXPECT().RegisterClient(mock.Anything, "claude-code").Return(true, nil).Once()
	m.EXPECT().CreateSession(mock.Anything, "claude-code", 60, mock.Anything).Return("tok-1", nil).Once()
	m.EXPECT().DeleteSession(mock.Anything, "tok-1").Return(nil).Once()
	m.EXPECT().Search(mock.Anything, "tok-1", mock.Anything).Return(calm.SearchResults{}, nil).Once()

	h := newHarness(t, m)
	initSession(t, h, "claude-code")

	res := callSearch(t, h, 2, map[string]any{"queries": []string{"nope"}, "source": src})
	if res.IsError {
		t.Fatalf("empty results must not be an error: %+v", res)
	}
	if got := resultText(t, res); !strings.Contains(got, "no matches under source="+src) {
		t.Errorf("text = %q; want the source scope noted", got)
	}
}

// A 404 from CALM's search triggers AD03 recovery: replacement created, but
// the original retrieval call still fails with the session_lost phrasing
// (retrieval has no raw fallback); the follow-up search runs against the
// replacement session.
func TestSearch_SessionLost_RecoversAndSignals(t *testing.T) {
	m := calm.NewMockClient(t)
	m.EXPECT().RegisterClient(mock.Anything, "claude-code").Return(true, nil).Once()

	var mu sync.Mutex
	var creates int
	m.EXPECT().CreateSession(mock.Anything, "claude-code", 60, mock.Anything).RunAndReturn(
		func(context.Context, string, int, string) (string, error) {
			mu.Lock()
			creates++
			n := creates
			mu.Unlock()
			if n == 1 {
				return "tok-1", nil
			}
			return "tok-2", nil
		},
	).Times(2)
	m.EXPECT().Search(mock.Anything, "tok-1", mock.Anything).
		Return(calm.SearchResults{}, &calm.StatusError{Op: "search", Code: 404, Status: "404 Not Found"}).Once()
	m.EXPECT().Search(mock.Anything, "tok-2", mock.Anything).Return(calm.SearchResults{}, nil).Once()
	m.EXPECT().DeleteSession(mock.Anything, "tok-2").Return(nil).Once()

	h := newHarness(t, m)
	initSession(t, h, "claude-code")

	res := callSearch(t, h, 2, map[string]any{"queries": []string{"zphlox"}})
	if !res.IsError {
		t.Fatalf("session-lost search must be an error result: %+v", res)
	}
	if got := resultText(t, res); got != obs.DegradedPhrase(obs.DegradedReasonSessionLost) {
		t.Errorf("text = %q; want session_lost phrasing", got)
	}

	res = callSearch(t, h, 3, map[string]any{"queries": []string{"zphlox"}})
	if res.IsError {
		t.Fatalf("post-recovery search errored: %+v", res)
	}
	if got := resultText(t, res); !strings.Contains(got, "no matches") {
		t.Errorf("post-recovery search = %q; want it to reach CALM (tok-2) and report no matches", got)
	}
}

// A direct 401 on search is auth_failed without a recovery attempt — CALM
// rejects credentials before session resolution, so a recreate would prove
// nothing. Strict mocks: only the initialize CreateSession is expected.
func TestSearch_Direct401_AuthFailed(t *testing.T) {
	m := calm.NewMockClient(t)
	m.EXPECT().RegisterClient(mock.Anything, "claude-code").Return(true, nil).Once()
	m.EXPECT().CreateSession(mock.Anything, "claude-code", 60, mock.Anything).Return("tok-1", nil).Once()
	m.EXPECT().Search(mock.Anything, "tok-1", mock.Anything).
		Return(calm.SearchResults{}, &calm.StatusError{Op: "search", Code: 401, Status: "401 Unauthorized"}).Once()
	// No DeleteSession: the latch clears the session, so shutdown has nothing to delete.

	h := newHarness(t, m)
	initSession(t, h, "claude-code")

	res := callSearch(t, h, 2, map[string]any{"queries": []string{"zphlox"}})
	if !res.IsError {
		t.Fatalf("auth-failed search must be an error result: %+v", res)
	}
	if got := resultText(t, res); got != obs.DegradedPhrase(obs.DegradedReasonAuthFailed) {
		t.Errorf("text = %q; want auth_failed phrasing", got)
	}

	// Sticky: the second search short-circuits with zero CALM traffic.
	res = callSearch(t, h, 3, map[string]any{"queries": []string{"zphlox"}})
	if got := resultText(t, res); got != obs.DegradedPhrase(obs.DegradedReasonAuthFailed) {
		t.Errorf("second search text = %q; want short-circuited auth_failed", got)
	}
}
