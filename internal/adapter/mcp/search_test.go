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

func TestSearch_CalmDownIsError(t *testing.T) {
	// No initialize → no session token. Zero mock expectations, so any Search call fails.
	m := calm.NewMockClient(t)
	h := newHarness(t, m)

	res := callSearch(t, h, 1, map[string]any{"queries": []string{"x"}})
	if !res.IsError {
		t.Fatalf("CALM-down must be an error result: %+v", res)
	}
	if got := resultText(t, res); !strings.Contains(got, "search unavailable") {
		t.Errorf("text = %q; want 'search unavailable'", got)
	}
}

func TestSearch_SearchErrorIsError(t *testing.T) {
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
	if got := resultText(t, res); !strings.Contains(got, "search unavailable") {
		t.Errorf("text = %q; want 'search unavailable'", got)
	}
}

func TestSearch_BlankQueriesIsError(t *testing.T) {
	m := calm.NewMockClient(t)
	h := newHarness(t, m)

	res := callSearch(t, h, 1, map[string]any{"queries": []string{"  "}})
	if !res.IsError {
		t.Fatalf("blank queries must be an error result: %+v", res)
	}
	if got := resultText(t, res); !strings.Contains(got, "queries is required") {
		t.Errorf("text = %q; want 'queries is required'", got)
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
