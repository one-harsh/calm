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
	"github.com/one-harsh/calm/internal/adapter/capture"
	"github.com/one-harsh/calm/internal/adapter/obs"
)

const toolNameSearch = "calm_search"

const searchTimeout = 10 * time.Second

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

	// Fused-token validation keeps stale content distinct from backend failure.
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
	if res.CorrelationID != "" {
		logging.BindSummary(ctx, obs.CorrelationID(res.CorrelationID))
	}

	// Empty pages are healthy retrieval results, not degradation.
	var out string
	if documentOrder {
		out = capture.FormatDocumentOrder(res, a.Offset, a.Source, searchVocab)
	} else {
		out = capture.FormatSearchResults(res, a.Source, searchVocab)
	}
	logging.BindSummary(ctx, obs.ResponseRawBytes(len(out)))
	return TextResult(out, false), nil
}

var searchVocab = capture.SearchVocab{
	TruncatedMarker:  "[truncated — raise budget_bytes or use a ranked query for the rest]",
	ContinuationLine: "more chunks remain — call calm_search again with source and offset: ",
	ZeroHitRanked:    "no matches",
	ZeroHitDocument:  "no chunks at this offset",
}
