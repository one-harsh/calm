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
	"github.com/one-harsh/calm/internal/adapter/obs"
)

const toolNameSearch = "calm_search"

const searchTimeout = 10 * time.Second

// CALM's /v1/search bounds per openapi.yaml SearchRequest.
const (
	maxSearchQueries = 10
	maxSearchLimit   = 50
)

const searchDescription = "Retrieve tool output already captured into CALM this session as ranked, " +
	"verbatim snippets. Prefer this over re-running a command to see its output again. Pass one or more " +
	"queries; optionally scope to a single source label with `source` (the label calm_run_command returns) " +
	"and cap hits per query with `limit`."

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
		Annotations: readOnlyAnnotations,
	}
}

// TODO: support queries-empty + source-scope shape per DESIGN.md AD01
// (sequential reread of captured content in document order). Currently
// requires non-empty queries and forwards ranked-retrieval semantics only.
// Blocks on the CALM-side /v1/search document-order extension.
func (s *Server) search(ctx context.Context, args json.RawMessage) (ToolResult, error) {
	var a searchArgs
	if err := json.Unmarshal(args, &a); err != nil {
		return ToolResult{}, &ArgError{Detail: err.Error()}
	}
	if len(a.Queries) == 0 {
		return ToolResult{}, &ArgError{Detail: "queries is required"}
	}
	if len(a.Queries) > maxSearchQueries {
		return ToolResult{}, &ArgError{Detail: fmt.Sprintf("too many queries (max %d, got %d)", maxSearchQueries, len(a.Queries))}
	}
	for i, q := range a.Queries {
		if strings.TrimSpace(q) == "" {
			return ToolResult{}, &ArgError{Detail: fmt.Sprintf("queries[%d] is blank (all queries must be non-empty per CALM's SearchRequest schema)", i)}
		}
	}
	if a.Limit < 0 || a.Limit > maxSearchLimit {
		return ToolResult{}, &ArgError{Detail: fmt.Sprintf("limit out of range (allowed 0..%d, got %d)", maxSearchLimit, a.Limit)}
	}

	ctx = logging.Bind(ctx, logging.StringField("source", a.Source))
	token, authFailed := s.sessionState()
	if authFailed {
		return ToolResult{IsError: true}, &DegradedSignal{Reason: obs.DegradedReasonAuthFailed}
	}
	if token == "" {
		s.log.WithContext(ctx).Warn(
			"search unavailable; CALM not connected",
			obs.DegradedReasonFieldCalmUnreachable,
		)
		return ToolResult{IsError: true}, &DegradedSignal{Reason: obs.DegradedReasonCalmUnreachable}
	}

	// Strip and validate the fused staleness suffix per LABELING.md §2 before
	// forwarding — CALM's grammar doesn't parse `@<token>`, and a stale token
	// must resolve locally as session_lost rather than reach CALM and come
	// back as calm_unreachable. Base-only labels pass through unchanged.
	calmSource := a.Source
	if a.Source != "" {
		stripped, ok := s.registry.ValidateAndStrip(a.Source)
		if !ok {
			s.log.WithContext(ctx).Warn(
				"stale source token; rejecting locally",
				obs.DegradedReasonFieldSessionLost,
			)
			return ToolResult{IsError: true}, &DegradedSignal{Reason: obs.DegradedReasonSessionLost}
		}
		calmSource = stripped
	}

	sctx, cancel := context.WithTimeout(ctx, searchTimeout)
	defer cancel()
	res, err := s.calm.Search(sctx, token, calm.SearchInput{Queries: a.Queries, Source: calmSource, Limit: a.Limit})
	if err != nil {
		if sig := s.sessionFailureSignal(ctx, token, err); sig != nil {
			return ToolResult{IsError: true}, sig
		}
		s.log.WithContext(ctx).Warn(
			"search failed",
			obs.DegradedReasonFieldCalmUnreachable,
			logging.ErrorField(err),
		)
		return ToolResult{IsError: true}, &DegradedSignal{Reason: obs.DegradedReasonCalmUnreachable, Detail: err.Error()}
	}

	if totalHits(res) == 0 {
		return TextResult("no matches"+sourceNote(a.Source), false), nil
	}
	return TextResult(formatSearchResults(res), false), nil
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
