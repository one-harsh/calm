// Copyright 2026 The CALM Authors
// SPDX-License-Identifier: Apache-2.0

package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	logging "github.com/one-harsh/context-logging"

	"github.com/one-harsh/calm/internal/adapter/calm"
)

const toolNameSearch = "calm_search"

const searchTimeout = 10 * time.Second

const searchDescription = "Search the tool output captured into CALM this session and get back ranked, " +
	"verbatim snippets. Pass one or more queries; optionally scope to a single source label with " +
	"`source` (the label calm_run_command returns) and cap hits per query with `limit`. Use this to " +
	"retrieve earlier command output on demand instead of re-running it."

const searchSchema = `{
  "type": "object",
  "properties": {
    "queries": {"type": "array", "items": {"type": "string"}, "description": "One or more search queries (terms or phrases)."},
    "source": {"type": "string", "description": "Optional source label to scope the search to one identity (e.g. from calm_run_command)."},
    "limit": {"type": "integer", "description": "Optional maximum number of hits per query."}
  },
  "required": ["queries"],
  "additionalProperties": false
}`

type searchArgs struct {
	Queries []string `json:"queries"`
	Source  string   `json:"source"`
	Limit   int      `json:"limit"`
}

func (s *Server) newSearchTool() Tool {
	return Tool{
		Name:        toolNameSearch,
		Description: searchDescription,
		InputSchema: json.RawMessage(searchSchema),
		Handler:     s.search,
	}
}

func (s *Server) search(ctx context.Context, args json.RawMessage) (ToolResult, error) {
	var a searchArgs
	if err := json.Unmarshal(args, &a); err != nil {
		return TextResult("invalid arguments: "+err.Error(), true), nil
	}
	if !hasNonBlank(a.Queries) {
		return TextResult("queries is required", true), nil
	}

	token := s.sessionToken()
	if token == "" {
		s.log.WithContext(ctx).Warn("search unavailable; CALM not connected")
		return TextResult("search unavailable: CALM not connected", true), nil
	}

	sctx, cancel := context.WithTimeout(ctx, searchTimeout)
	defer cancel()
	res, err := s.calm.Search(sctx, token, calm.SearchInput{Queries: a.Queries, Source: a.Source, Limit: a.Limit})
	if err != nil {
		s.log.WithContext(ctx).Warn("search failed", logging.ErrorField(err))
		return TextResult("search unavailable: "+err.Error(), true), nil
	}

	if totalHits(res) == 0 {
		return TextResult("no matches"+sourceNote(a.Source), false), nil
	}
	return TextResult(formatSearchResults(res), false), nil
}

func hasNonBlank(queries []string) bool {
	for _, q := range queries {
		if strings.TrimSpace(q) != "" {
			return true
		}
	}
	return false
}

func totalHits(res calm.SearchResults) int {
	n := 0
	for _, q := range res.Queries {
		n += len(q.Hits)
	}
	return n
}

func sourceNote(source string) string {
	if source == "" {
		return ""
	}
	return " under source=" + source
}

const maxSearchResultLen = 8192

func formatSearchResults(res calm.SearchResults) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%d %s across %d %s:\n",
		totalHits(res), plural(totalHits(res), "hit", "hits"),
		len(res.Queries), plural(len(res.Queries), "query", "queries"))

	for _, q := range res.Queries {
		fmt.Fprintf(&b, "\n# %q — %d %s\n", q.Query, len(q.Hits), plural(len(q.Hits), "hit", "hits"))
		for _, h := range q.Hits {
			fmt.Fprintf(&b, "[%s] %s  (%s)\n%s\n", h.MatchLayer, h.Title, h.Source, h.Snippet)
		}
	}

	out := b.String()
	if len(out) > maxSearchResultLen {
		out = strings.ToValidUTF8(out[:maxSearchResultLen], "") + "…"
	}
	return out
}

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}
