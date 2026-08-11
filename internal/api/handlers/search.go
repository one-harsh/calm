// Copyright 2026 The CALM Authors
// SPDX-License-Identifier: Apache-2.0

package handlers

import (
	"context"
	"errors"
	"net/http"

	logging "github.com/one-harsh/context-logging"

	"github.com/one-harsh/calm/internal/api/genapi"
	"github.com/one-harsh/calm/internal/auth"
	"github.com/one-harsh/calm/internal/db"
	"github.com/one-harsh/calm/internal/search"
	"github.com/one-harsh/calm/internal/session"
)

const (
	defaultSearchLimit       = 5
	searchDefaultBudgetBytes = 4096
)

func (h *Handlers) Search(
	ctx context.Context,
	request genapi.SearchRequestObject,
) (genapi.SearchResponseObject, error) {
	if request.Body == nil {
		return genapi.Search400JSONResponse{BadRequestJSONResponse: genapi.BadRequestJSONResponse{
			Error:  "invalid_request",
			Detail: ptr("missing request body"),
		}}, nil
	}

	md, ok := session.MetadataFromContext(ctx)
	if !ok {
		h.deps.Logger.WithContext(ctx).Error("search: session metadata missing from context")
		return nil, errors.New("session metadata not present in request context")
	}
	namespace := auth.NamespaceFromContext(ctx)
	correlationID := correlationIDOrNil(ctx, h.deps.Logger, "search")

	queries := request.Body.Queries
	source := ""
	if request.Body.Source != nil {
		source = *request.Body.Source
	}
	budget := h.searchBudget(ctx, request.Body.BudgetBytes)

	var result search.Result
	var err error
	if len(queries) == 0 {
		if source == "" {
			return genapi.Search400JSONResponse{BadRequestJSONResponse: genapi.BadRequestJSONResponse{
				Error:  "invalid_request",
				Detail: ptr("either queries or source is required"),
			}}, nil
		}
		offset := 0
		if request.Body.Offset != nil && *request.Body.Offset > 0 {
			offset = *request.Body.Offset
		}
		limit := 0
		if request.Body.Limit != nil {
			limit = *request.Body.Limit
		}
		result, err = h.deps.Search.DocumentOrder(ctx, namespace, md.ID, correlationID, source, limit, offset, budget)
	} else {
		limit := defaultSearchLimit
		if request.Body.Limit != nil {
			limit = *request.Body.Limit
		}
		in := db.SearchInput{SessionID: md.ID, Source: source, Limit: limit}
		variant := h.resolveAllocator(namespace, request.Params.XCALMAllocatorVariant)
		result, err = h.deps.Search.Search(ctx, namespace, md.ID, correlationID, in, queries, budget, variant)
	}
	if err != nil {
		if m, ok := mapSearchError(err); ok {
			body := genapi.Error{Error: m.Code, Detail: &m.Detail}
			switch m.Status {
			case http.StatusNotFound:
				return genapi.Search404JSONResponse{NotFoundJSONResponse: genapi.NotFoundJSONResponse(body)}, nil
			case http.StatusBadRequest:
				return genapi.Search400JSONResponse{BadRequestJSONResponse: genapi.BadRequestJSONResponse(body)}, nil
			default:
				h.deps.Logger.WithContext(ctx).Warn(
					"search: mapped sentinel has no response variant — returning 500",
					logging.IntField("http.status", m.Status),
					logging.StringField("error.code", m.Code),
					logging.ErrorField(err),
				)
			}
		}
		if !isContextError(err) {
			h.deps.Logger.WithContext(ctx).Error("search failed", logging.ErrorField(err))
		}
		return nil, err
	}

	out := make([]genapi.QuerySearchResult, len(result.Queries))
	for qi, r := range result.Queries {
		hits := make([]genapi.SearchHit, len(r.Hits))
		for i, hit := range r.Hits {
			gh := genapi.SearchHit{
				Title:      hit.Title,
				Snippet:    hit.Snippet,
				Source:     hit.Source,
				MatchLayer: genapi.SearchHitMatchLayer(hit.MatchLayer),
			}
			if hit.Truncated {
				t := true
				gh.Truncated = &t
			}
			hits[i] = gh
		}
		out[qi] = genapi.QuerySearchResult{Query: r.Query, Hits: hits, BudgetOmitted: r.BudgetOmitted}
	}

	h.deps.Logger.WithContext(ctx).Debug("search completed", logging.IntField("search.queries", len(result.Queries)))

	return genapi.Search200JSONResponse(genapi.SearchResult{
		Results:        out,
		ByteBudgetUsed: result.ByteBudgetUsed,
		BudgetExceeded: result.BudgetExceeded,
		BudgetBytes:    result.BudgetBytes,
		NextOffset:     result.NextOffset,
	}), nil
}

// Clamps to the operator ceiling with a WARN, never a 400 (DL15 clamp-not-reject).
func (h *Handlers) searchBudget(ctx context.Context, requested *int) int {
	budget := searchDefaultBudgetBytes
	if requested != nil && *requested > 0 {
		budget = *requested
	}
	if ceiling := h.deps.Cfg.SearchMaxBudgetBytes; ceiling > 0 && budget > ceiling {
		h.deps.Logger.WithContext(ctx).Warn(
			"search: budget_bytes clamped to ceiling",
			logging.IntField("search.requested_budget_bytes", budget),
			logging.IntField("search.committed_budget_bytes", ceiling),
		)
		budget = ceiling
	}
	return budget
}

// An unrecognized or disallowed override header is silently ignored — it is a
// hint, never a 400.
func (h *Handlers) resolveAllocator(namespace string, header *string) search.Variant {
	variant := search.VariantRankRound
	if def, ok := h.deps.Registry.DefaultAllocatorFor(namespace); ok {
		if v, ok := search.ParseVariant(def); ok {
			variant = v
		}
	}
	if header != nil && h.deps.Registry.AllowAllocatorOverride(namespace) {
		if v, ok := search.ParseVariant(*header); ok {
			variant = v
		}
	}
	return variant
}
