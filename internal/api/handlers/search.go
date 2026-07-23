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

	in := db.SearchInput{SessionID: md.ID, Limit: defaultSearchLimit}
	if request.Body.Source != nil {
		in.Source = *request.Body.Source
	}
	if request.Body.Limit != nil {
		in.Limit = *request.Body.Limit
	}

	budget := h.searchBudget(ctx, request.Body.BudgetBytes)
	variant := h.resolveAllocator(namespace, request.Params.XCALMAllocatorVariant)

	result, err := h.deps.Search.Search(ctx, namespace, md.ID, correlationID, in, request.Body.Queries, budget, variant)
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
			hits[i] = genapi.SearchHit{
				Title:      hit.Title,
				Snippet:    hit.Snippet,
				Source:     hit.Source,
				MatchLayer: genapi.SearchHitMatchLayer(hit.MatchLayer),
			}
		}
		out[qi] = genapi.QuerySearchResult{Query: r.Query, Hits: hits, BudgetOmitted: r.BudgetOmitted}
	}

	h.deps.Logger.WithContext(ctx).Debug("search completed", logging.IntField("search.queries", len(result.Queries)))

	return genapi.Search200JSONResponse(genapi.SearchResult{
		Results:        out,
		ByteBudgetUsed: result.ByteBudgetUsed,
		BudgetExceeded: result.BudgetExceeded,
		BudgetBytes:    result.BudgetBytes,
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
