// Copyright 2026 The CALM Authors
// SPDX-License-Identifier: Apache-2.0

package handlers

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	logging "github.com/one-harsh/context-logging"

	"github.com/one-harsh/calm/internal/api/genapi"
	"github.com/one-harsh/calm/internal/auth"
	"github.com/one-harsh/calm/internal/db"
	"github.com/one-harsh/calm/internal/obs"
)

func (h *Handlers) CreateSession(
	ctx context.Context,
	request genapi.CreateSessionRequestObject,
) (genapi.CreateSessionResponseObject, error) {
	if request.Body == nil {
		return genapi.CreateSession400JSONResponse{BadRequestJSONResponse: genapi.BadRequestJSONResponse{
			Error:  "invalid_request",
			Detail: ptr("missing request body"),
		}}, nil
	}

	namespace := auth.NamespaceFromContext(ctx)
	if namespace == "" {
		h.deps.Logger.WithContext(ctx).Error("namespace missing from request context — auth middleware did not run")
		return nil, errors.New("namespace not present in request context")
	}
	ctx = logging.Bind(ctx, obs.SessionID(request.Body.SessionId))

	sess := &db.Session{
		ID:         request.Body.SessionId,
		Namespace:  namespace,
		TTLMinutes: h.deps.Cfg.DefaultTTLMinutes,
	}
	if request.Body.TtlMinutes != nil {
		sess.TTLMinutes = *request.Body.TtlMinutes
	}
	if sess.TTLMinutes > h.deps.Cfg.MaxTTLMinutes {
		h.deps.Logger.WithContext(ctx).Warn("session.create.ttl_minutes clamped to operator ceiling",
			logging.IntField("session.create.requested_ttl_minutes", sess.TTLMinutes),
			logging.IntField("session.create.committed_ttl_minutes", h.deps.Cfg.MaxTTLMinutes),
		)
		sess.TTLMinutes = h.deps.Cfg.MaxTTLMinutes
	}
	if request.Body.Client != nil {
		sess.Client = *request.Body.Client
	}
	if request.Body.Labels != nil {
		sess.Labels = *request.Body.Labels
	}

	if err := h.deps.Sessions.Create(ctx, sess); err != nil {
		if m, ok := mapSessionError(err); ok {
			detail := m.Detail
			if m.Status == http.StatusConflict {
				detail = fmt.Sprintf("session %q already exists in namespace %q", sess.ID, namespace)
			}
			body := genapi.Error{Error: m.Code, Detail: &detail}
			switch m.Status {
			case http.StatusConflict:
				return genapi.CreateSession409JSONResponse{ConflictJSONResponse: genapi.ConflictJSONResponse(body)}, nil
			case http.StatusBadRequest:
				return genapi.CreateSession400JSONResponse{BadRequestJSONResponse: genapi.BadRequestJSONResponse(body)}, nil
			}
		}
		if !isContextError(err) {
			h.deps.Logger.WithContext(ctx).Error("create session failed",
				logging.ErrorField(err),
			)
		}
		return nil, err
	}

	return genapi.CreateSession201JSONResponse(genapi.Session{
		SessionId:  sess.ID,
		Namespace:  sess.Namespace,
		Client:     sess.Client,
		TtlMinutes: sess.TTLMinutes,
		CreatedAt:  sess.CreatedAt,
	}), nil
}

func (h *Handlers) DeleteSession(
	ctx context.Context,
	request genapi.DeleteSessionRequestObject,
) (genapi.DeleteSessionResponseObject, error) {
	namespace := auth.NamespaceFromContext(ctx)
	if namespace == "" {
		h.deps.Logger.WithContext(ctx).Error("namespace missing from request context — auth middleware did not run")
		return nil, errors.New("namespace not present in request context")
	}

	sessionID := string(request.SessionId)
	ctx = logging.Bind(ctx, obs.SessionID(sessionID))

	result, err := h.deps.Sessions.Delete(ctx, namespace, sessionID)
	if err != nil {
		if m, ok := mapSessionError(err); ok && m.Status == http.StatusNotFound {
			detail := fmt.Sprintf("session %q not found in namespace %q", sessionID, namespace)
			return genapi.DeleteSession404JSONResponse{NotFoundJSONResponse: genapi.NotFoundJSONResponse{
				Error:  m.Code,
				Detail: &detail,
			}}, nil
		}
		if !isContextError(err) {
			h.deps.Logger.WithContext(ctx).Error("delete session failed",
				logging.ErrorField(err),
			)
		}
		return nil, err
	}

	h.deps.Logger.WithContext(ctx).Info("session closed",
		obs.CloseReasonExplicit,
		logging.IntField("session.delete.cascaded_events", result.Cascaded.Events),
		logging.IntField("session.delete.cascaded_sources", result.Cascaded.Sources),
		logging.IntField("session.delete.cascaded_chunks", result.Cascaded.Chunks),
		logging.IntField("session.delete.cascaded_labels", result.Cascaded.Labels),
	)

	return genapi.DeleteSession200JSONResponse(genapi.DeleteSessionResult{
		DeletedSessionId: result.SessionID,
		Cascaded: genapi.CascadeCounts{
			Sources: result.Cascaded.Sources,
			Chunks:  result.Cascaded.Chunks,
			Events:  result.Cascaded.Events,
			Labels:  result.Cascaded.Labels,
		},
	}), nil
}

func (h *Handlers) GetSnapshot(_ context.Context, _ genapi.GetSnapshotRequestObject) (genapi.GetSnapshotResponseObject, error) {
	return nil, ErrNotImplemented
}

func (h *Handlers) ListSources(_ context.Context, _ genapi.ListSourcesRequestObject) (genapi.ListSourcesResponseObject, error) {
	return nil, ErrNotImplemented
}
