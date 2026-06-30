// Copyright 2026 The CALM Authors
// SPDX-License-Identifier: Apache-2.0

package mcp_test

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/mock"

	"github.com/one-harsh/calm/internal/adapter/calm"
	"github.com/one-harsh/calm/internal/adapter/mcp"
	"github.com/one-harsh/calm/internal/adapter/obs"
)

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
	m.EXPECT().CreateSession(mock.Anything, "claude-code", 60).Return("tok-1", nil).Once()
	m.EXPECT().DeleteSession(mock.Anything, "tok-1").Return(nil).Once()
	m.EXPECT().Search(mock.Anything, "tok-1", mock.MatchedBy(func(in calm.SearchInput) bool {
		return len(in.Queries) == 1 && in.Queries[0] == "zphlox" && in.Source == "" && in.Limit == 0
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
	m.EXPECT().CreateSession(mock.Anything, "claude-code", 60).Return("tok-1", nil).Once()
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

func TestSearch_PassesLimit(t *testing.T) {
	m := calm.NewMockClient(t)
	m.EXPECT().RegisterClient(mock.Anything, "claude-code").Return(true, nil).Once()
	m.EXPECT().CreateSession(mock.Anything, "claude-code", 60).Return("tok-1", nil).Once()
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
	m.EXPECT().CreateSession(mock.Anything, "claude-code", 60).Return("tok-1", nil).Once()
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
	m.EXPECT().CreateSession(mock.Anything, "claude-code", 60).Return("tok-1", nil).Once()
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
	m.EXPECT().CreateSession(mock.Anything, "claude-code", 60).Return("tok-1", nil).Once()
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

func TestSearch_EmptyQueriesArrayIsArgError(t *testing.T) {
	m := calm.NewMockClient(t)
	h := newHarness(t, m)

	res := callSearch(t, h, 1, map[string]any{"queries": []string{}})
	if !res.IsError {
		t.Fatalf("empty queries array must be an error result: %+v", res)
	}
	if got := resultText(t, res); !strings.Contains(got, "queries is required") {
		t.Errorf("text = %q; want 'queries is required'", got)
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
	m.EXPECT().CreateSession(mock.Anything, "claude-code", 60).Return("tok-1", nil).Once()
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
