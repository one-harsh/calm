// Copyright 2026 The CALM Authors
// SPDX-License-Identifier: Apache-2.0

package handlers

import (
	"context"
	"errors"
	"fmt"

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
		switch {
		case errors.Is(err, db.ErrSessionExists):
			return genapi.CreateSession409JSONResponse{ConflictJSONResponse: genapi.ConflictJSONResponse{
				Error:  "session_exists",
				Detail: ptr(fmt.Sprintf("session %q already exists in namespace %q", sess.ID, namespace)),
			}}, nil
		case errors.Is(err, db.ErrSessionIDRequired), errors.Is(err, db.ErrInvalidTTL):
			return genapi.CreateSession400JSONResponse{BadRequestJSONResponse: genapi.BadRequestJSONResponse{
				Error:  "invalid_request",
				Detail: ptr(err.Error()),
			}}, nil
		case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
			return nil, err
		default:
			h.deps.Logger.WithContext(ctx).Error("create session failed",
				logging.ErrorField(err),
			)
			return nil, err
		}
	}

	return genapi.CreateSession201JSONResponse(genapi.Session{
		SessionId:  sess.ID,
		Namespace:  sess.Namespace,
		Client:     sess.Client,
		TtlMinutes: sess.TTLMinutes,
		CreatedAt:  sess.CreatedAt,
	}), nil
}

func (h *Handlers) DeleteSession(_ context.Context, _ genapi.DeleteSessionRequestObject) (genapi.DeleteSessionResponseObject, error) {
	return nil, ErrNotImplemented
}

func (h *Handlers) GetSnapshot(_ context.Context, _ genapi.GetSnapshotRequestObject) (genapi.GetSnapshotResponseObject, error) {
	return nil, ErrNotImplemented
}

func (h *Handlers) ListSources(_ context.Context, _ genapi.ListSourcesRequestObject) (genapi.ListSourcesResponseObject, error) {
	return nil, ErrNotImplemented
}
