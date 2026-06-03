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
	"github.com/one-harsh/calm/internal/session"
)

const defaultSearchLimit = 5

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

	in := db.SearchInput{SessionID: md.ID, Queries: request.Body.Queries, Limit: defaultSearchLimit}
	if request.Body.Source != nil {
		in.Source = *request.Body.Source
	}
	if request.Body.Limit != nil {
		in.Limit = *request.Body.Limit
	}

	results, err := h.deps.Sources.Search(ctx, namespace, in)
	if err != nil {
		if m, ok := mapSearchError(err); ok {
			body := genapi.Error{Error: m.Code, Detail: &m.Detail}
			switch m.Status {
			case http.StatusNotFound:
				return genapi.Search404JSONResponse{NotFoundJSONResponse: genapi.NotFoundJSONResponse(body)}, nil
			case http.StatusBadRequest:
				return genapi.Search400JSONResponse{BadRequestJSONResponse: genapi.BadRequestJSONResponse(body)}, nil
			default:
				h.deps.Logger.WithContext(ctx).Warn("search: mapped sentinel has no response variant — returning 500",
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

	out := make([]genapi.QuerySearchResult, len(results))
	for qi, r := range results {
		hits := make([]genapi.SearchHit, len(r.Hits))
		for i, hit := range r.Hits {
			hits[i] = genapi.SearchHit{
				Title:      hit.Title,
				Snippet:    hit.Snippet,
				Source:     hit.Source,
				MatchLayer: genapi.SearchHitMatchLayer(hit.MatchLayer),
			}
		}
		out[qi] = genapi.QuerySearchResult{Query: r.Query, Hits: hits}
	}

	h.deps.Logger.WithContext(ctx).Debug("search completed", logging.IntField("search.queries", len(results)))

	return genapi.Search200JSONResponse(genapi.SearchResult{Results: out}), nil
}
