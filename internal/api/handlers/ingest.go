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
	"github.com/one-harsh/calm/internal/ingest"
	"github.com/one-harsh/calm/internal/obs"
	"github.com/one-harsh/calm/internal/session"
)

func (h *Handlers) Ingest(
	ctx context.Context,
	request genapi.IngestRequestObject,
) (genapi.IngestResponseObject, error) {
	if request.Body == nil {
		return genapi.Ingest400JSONResponse{BadRequestJSONResponse: genapi.BadRequestJSONResponse{
			Error:  "invalid_request",
			Detail: ptr("missing request body"),
		}}, nil
	}

	md, ok := session.MetadataFromContext(ctx)
	if !ok {
		h.deps.Logger.WithContext(ctx).Error("ingest: session metadata missing from context")
		return nil, errors.New("session metadata not present in request context")
	}
	namespace := auth.NamespaceFromContext(ctx)

	in := ingest.Input{Source: request.Body.Source, Content: request.Body.Content}
	if request.Body.Format != nil {
		in.Format = string(*request.Body.Format)
	}
	if request.Body.ContentType != nil {
		in.ContentType = *request.Body.ContentType
	}

	// HLD-DEVIATION: intent-driven RRF summary ordering + per-section `matches`
	// (HLD ingest section) needs search (deferred); the summary is returned in
	// document order and `matches` stays absent. WARN so the dropped signal is
	// observable rather than silent.
	if request.Body.Intents != nil && len(*request.Body.Intents) > 0 {
		h.deps.Logger.WithContext(ctx).Warn("ingest: intents provided but RRF ordering not implemented; returning document-order summary",
			logging.IntField("ingest.intents_provided", len(*request.Body.Intents)),
		)
	}

	result, err := h.deps.Ingest.Ingest(ctx, namespace, md.ID, in)
	if err != nil {
		if m, ok := mapIngestError(err); ok {
			body := genapi.Error{Error: m.Code, Detail: &m.Detail}
			switch m.Status {
			case http.StatusNotFound:
				return genapi.Ingest404JSONResponse{NotFoundJSONResponse: genapi.NotFoundJSONResponse(body)}, nil
			case http.StatusBadRequest:
				return genapi.Ingest400JSONResponse{BadRequestJSONResponse: genapi.BadRequestJSONResponse(body)}, nil
			default:
				h.deps.Logger.WithContext(ctx).Warn("ingest: mapped sentinel has no response variant — returning 500",
					logging.IntField("http.status", m.Status),
					logging.StringField("error.code", m.Code),
					logging.ErrorField(err),
				)
			}
		}
		if !isContextError(err) {
			h.deps.Logger.WithContext(ctx).Error("ingest failed", logging.ErrorField(err))
		}
		return nil, err
	}

	summary := make([]genapi.SectionPreview, len(result.Summary))
	for i, sec := range result.Summary {
		preview := sec.Preview
		summary[i] = genapi.SectionPreview{Title: sec.Title, Preview: &preview}
	}

	h.deps.Logger.WithContext(ctx).WithAuditEvent(logging.ResourceCreate).Info("content ingested",
		obs.Source(in.Source),
		logging.IntField("ingest.sections_indexed", result.SectionsIndexed),
		logging.IntField("ingest.sections_total", result.SectionsTotal),
	)

	return genapi.Ingest200JSONResponse(genapi.IngestResult{
		Source:           result.Source,
		SectionsIndexed:  result.SectionsIndexed,
		SectionsTotal:    result.SectionsTotal,
		SummaryTruncated: result.SummaryTruncated,
		Summary:          summary,
		DistinctiveTerms: result.DistinctiveTerms,
	}), nil
}
