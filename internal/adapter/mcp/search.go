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

const searchDescription = "Retrieve tool output already captured into CALM this session, verbatim. " +
	"Prefer this over re-running a command to see its output again. Two modes: with one or more `queries`, " +
	"returns relevance-ranked snippets, optionally scoped to a single `source` label (the label " +
	"calm_run_command returns) with `limit` hits per query. With a `source` and no `queries`, rereads that " +
	"source's captured content in original document order, paginated — pass `offset` to continue from where a " +
	"prior page ended (the output names the next offset). `budget_bytes` raises the response byte budget " +
	"(server default 4 KB) — re-request a truncated chunk's offset with a larger budget to get it whole. " +
	"Supply `queries`, `source`, or both."

const searchSchema = `{
  "type": "object",
  "properties": {
    "queries": {"type": "array", "items": {"type": "string"}, "description": "One or more search queries (terms or phrases). Omit for document-order reread of a source."},
    "source": {"type": "string", "description": "Source label to scope to one identity (e.g. from calm_run_command). Required, with queries omitted, for document-order reread."},
    "limit": {"type": "integer", "description": "Optional maximum number of hits per query (ranked) or chunks per page (document-order)."},
    "offset": {"type": "integer", "minimum": 0, "description": "Document-order only: zero-based chunk index to start the page from, for continuation. Ignored when queries are present."},
    "budget_bytes": {"type": "integer", "minimum": 1, "description": "Optional response byte budget — the server defaults it (4 KB) and clamps to the operator ceiling. Raise it to recover a truncated document-order chunk (re-request the same offset with a larger budget) or to fit more ranked hits."}
  },
  "additionalProperties": false
}`

type searchArgs struct {
	Queries     []string `json:"queries"`
	Source      string   `json:"source"`
	Limit       int      `json:"limit"`
	Offset      int      `json:"offset"`
	BudgetBytes int      `json:"budget_bytes"`
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

func (s *Server) search(ctx context.Context, args json.RawMessage) (ToolResult, error) {
	var a searchArgs
	if err := json.Unmarshal(args, &a); err != nil {
		return ToolResult{}, &ArgError{Detail: err.Error()}
	}
	if len(a.Queries) == 0 && a.Source == "" {
		return ToolResult{}, &ArgError{Detail: "queries or source is required"}
	}
	// Queries absent + source present selects document-order mode (sequential
	// reread); queries present is ranked retrieval, and offset is ignored.
	documentOrder := len(a.Queries) == 0
	if documentOrder {
		if a.Offset < 0 {
			return ToolResult{}, &ArgError{Detail: fmt.Sprintf("offset out of range (must be >= 0, got %d)", a.Offset)}
		}
	} else {
		if len(a.Queries) > maxSearchQueries {
			return ToolResult{}, &ArgError{Detail: fmt.Sprintf("too many queries (max %d, got %d)", maxSearchQueries, len(a.Queries))}
		}
		for i, q := range a.Queries {
			if strings.TrimSpace(q) == "" {
				return ToolResult{}, &ArgError{Detail: fmt.Sprintf("queries[%d] is blank (all queries must be non-empty per CALM's SearchRequest schema)", i)}
			}
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

	in := calm.SearchInput{Queries: a.Queries, Source: calmSource, Limit: a.Limit, BudgetBytes: a.BudgetBytes}
	if documentOrder {
		in.Offset = a.Offset
	}

	sctx, cancel := context.WithTimeout(ctx, searchTimeout)
	defer cancel()
	res, err := s.calm.Search(sctx, token, in)
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

	if documentOrder {
		// Offset-past-end is a healthy empty page, not a degradation — keep it
		// distinct from the calm_unreachable/session_lost error shapes.
		if totalHits(res) == 0 {
			return TextResult("no chunks at this offset"+sourceNote(a.Source), false), nil
		}
		return TextResult(formatDocumentOrder(res, a.Offset), false), nil
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

const (
	documentOrderTruncatedMarker  = "[truncated — raise budget_bytes or use a ranked query for the rest]"
	documentOrderContinuationLine = "more chunks remain — call calm_search again with source and offset: "
)

// formatDocumentOrder renders a document-order page as a sequential reread: no
// ranking annotations, each chunk its title line then full snippet text, in
// order. Unlike ranked results this is not adapter-capped — the page is already
// bounded by CALM's byte budget, and a local cap would sever the continuation
// hint and truncate the exact chunk text (content-fidelity).
func formatDocumentOrder(res calm.SearchResults, offset int) string {
	var hits []calm.Hit
	for _, q := range res.Queries {
		hits = append(hits, q.Hits...)
	}

	var b strings.Builder
	fmt.Fprintf(&b, "%d %s in document order from offset %d:\n",
		len(hits), plural(len(hits), "chunk", "chunks"), offset)
	for _, h := range hits {
		fmt.Fprintf(&b, "\n## %s\n%s\n", h.Title, h.Snippet)
		if h.Truncated {
			b.WriteString(documentOrderTruncatedMarker + "\n")
		}
	}
	if res.NextOffset != nil {
		fmt.Fprintf(&b, "\n%s%d\n", documentOrderContinuationLine, *res.NextOffset)
	}
	return b.String()
}

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}
