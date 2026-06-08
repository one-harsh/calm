// Copyright 2026 The CALM Authors
// SPDX-License-Identifier: Apache-2.0

package handlers

import (
	"context"
	"errors"
	"net/http"

	"github.com/google/uuid"
	logging "github.com/one-harsh/context-logging"

	"github.com/one-harsh/calm/internal/api/genapi"
	"github.com/one-harsh/calm/internal/auth"
	"github.com/one-harsh/calm/internal/obs"
	"github.com/one-harsh/calm/internal/session"
)

const defaultFeedbackTTLMinutes = 60

func (h *Handlers) Feedback(ctx context.Context, request genapi.FeedbackRequestObject) (genapi.FeedbackResponseObject, error) {
	namespace := auth.NamespaceFromContext(ctx)
	if namespace == "" {
		h.deps.Logger.WithContext(ctx).Error("feedback: namespace missing from context")
		return nil, errors.New("namespace not present in request context")
	}

	md, ok := session.MetadataFromContext(ctx)
	if !ok {
		// SessionResolve runs ahead per the spec's required session-token header.
		h.deps.Logger.WithContext(ctx).Error("feedback: session metadata missing from context")
		return nil, errors.New("session metadata not present in request context")
	}

	correlationID := uuid.UUID(request.Body.CorrelationId)

	ttl, _ := h.deps.Registry.FeedbackTTLFor(namespace)
	if ttl == 0 {
		ttl = defaultFeedbackTTLMinutes
	}

	if _, err := h.deps.Feedback.Receive(ctx, namespace, md.ID, correlationID, string(request.Body.Outcome), ttl); err != nil {
		if m, mapped := mapFeedbackError(err); mapped {
			switch m.Status {
			case http.StatusGone:
				return genapi.Feedback410JSONResponse{GoneJSONResponse: genapi.GoneJSONResponse{
					Error: m.Code, Detail: &m.Detail,
				}}, nil
			case http.StatusNotFound:
				return genapi.Feedback404JSONResponse{NotFoundJSONResponse: genapi.NotFoundJSONResponse{
					Error: m.Code, Detail: &m.Detail,
				}}, nil
			case http.StatusConflict:
				return genapi.Feedback409JSONResponse{ConflictJSONResponse: genapi.ConflictJSONResponse{
					Error: m.Code, Detail: &m.Detail,
				}}, nil
			case http.StatusBadRequest:
				return genapi.Feedback400JSONResponse{BadRequestJSONResponse: genapi.BadRequestJSONResponse{
					Error: m.Code, Detail: &m.Detail,
				}}, nil
			case http.StatusServiceUnavailable:
				return genapi.Feedback503JSONResponse{ServiceUnavailableJSONResponse: genapi.ServiceUnavailableJSONResponse{
					Error: m.Code, Detail: &m.Detail,
				}}, nil
			}
		}
		if !isContextError(err) {
			h.deps.Logger.WithContext(ctx).Error("feedback receive failed", logging.ErrorField(err))
		}
		return nil, err
	}

	// Don't rebind correlation_id — the Context middleware already bound this
	// request's own correlation_id. The body's correlation_id refers to a prior
	// call and goes under a distinct key so the audit row carries both.
	h.deps.Logger.WithContext(ctx).WithAuditEvent(logging.ResourceCreate).Info(
		"feedback received",
		obs.AuditInitiatorAPI,
		obs.Client(md.Client),
		obs.FeedbackOutcome(string(request.Body.Outcome)),
		obs.ReferencedCorrelationID(correlationID.String()),
	)

	return genapi.Feedback204Response{}, nil
}
