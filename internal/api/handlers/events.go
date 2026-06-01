// Copyright 2026 The CALM Authors
// SPDX-License-Identifier: Apache-2.0

package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"

	logging "github.com/one-harsh/context-logging"

	"github.com/one-harsh/calm/internal/api/genapi"
	"github.com/one-harsh/calm/internal/auth"
	"github.com/one-harsh/calm/internal/db"
	"github.com/one-harsh/calm/internal/obs"
	"github.com/one-harsh/calm/internal/session"
)

func (h *Handlers) WriteEvents(
	ctx context.Context,
	request genapi.WriteEventsRequestObject,
) (genapi.WriteEventsResponseObject, error) {
	if request.Body == nil {
		return genapi.WriteEvents400JSONResponse{BadRequestJSONResponse: genapi.BadRequestJSONResponse{
			Error:  "invalid_request",
			Detail: ptr("missing request body"),
		}}, nil
	}

	md, ok := session.MetadataFromContext(ctx)
	if !ok {
		h.deps.Logger.WithContext(ctx).Error("write events: session metadata missing from context")
		return nil, errors.New("session metadata not present in request context")
	}
	namespace := auth.NamespaceFromContext(ctx)

	inputs := make([]db.EventInput, 0, len(request.Body.Events))
	for i, ev := range request.Body.Events {
		data, err := json.Marshal(ev.Data)
		if err != nil {
			detail := fmt.Sprintf("events[%d].data: %v", i, err)
			return genapi.WriteEvents400JSONResponse{BadRequestJSONResponse: genapi.BadRequestJSONResponse{
				Error:  "invalid_request",
				Detail: &detail,
			}}, nil
		}
		inputs = append(inputs, db.EventInput{
			Type:     ev.Type,
			Priority: ev.Priority,
			Data:     data,
		})
	}

	accepted, err := h.deps.Events.Write(ctx, namespace, md.ID, inputs)
	if err != nil {
		if m, ok := mapEventsError(err); ok {
			body := genapi.Error{Error: m.Code, Detail: &m.Detail}
			switch m.Status {
			case http.StatusNotFound:
				return genapi.WriteEvents404JSONResponse{NotFoundJSONResponse: genapi.NotFoundJSONResponse(body)}, nil
			case http.StatusBadRequest:
				return genapi.WriteEvents400JSONResponse{BadRequestJSONResponse: genapi.BadRequestJSONResponse(body)}, nil
			default:
				h.deps.Logger.WithContext(ctx).Warn("write events: mapped sentinel has no response variant — returning 500",
					logging.IntField("http.status", m.Status),
					logging.StringField("error.code", m.Code),
					logging.ErrorField(err),
				)
			}
		}
		if !isContextError(err) {
			h.deps.Logger.WithContext(ctx).Error("write events failed", logging.ErrorField(err))
		}
		return nil, err
	}

	h.deps.Logger.WithContext(ctx).Debug("events written",
		logging.IntField("events.write.accepted", accepted),
		logging.IntField("events.write.submitted", len(inputs)),
	)

	return genapi.WriteEvents202JSONResponse(genapi.WriteEventsResult{
		Accepted: accepted,
	}), nil
}

func (h *Handlers) ReadEvents(
	ctx context.Context,
	request genapi.ReadEventsRequestObject,
) (genapi.ReadEventsResponseObject, error) {
	md, ok := session.MetadataFromContext(ctx)
	if !ok {
		h.deps.Logger.WithContext(ctx).Error("read events: session metadata missing from context")
		return nil, errors.New("session metadata not present in request context")
	}
	namespace := auth.NamespaceFromContext(ctx)

	filter := db.EventFilter{}
	if request.Params.Types != nil {
		filter.Types = *request.Params.Types
	}
	if request.Params.MinPriority != nil {
		filter.MinPriority = *request.Params.MinPriority
	}
	if request.Params.Limit != nil {
		filter.Limit = *request.Params.Limit
	}

	events, err := h.deps.Events.Read(ctx, namespace, md.ID, filter)
	if err != nil {
		if m, ok := mapEventsError(err); ok {
			body := genapi.Error{Error: m.Code, Detail: &m.Detail}
			switch m.Status {
			case http.StatusNotFound:
				return genapi.ReadEvents404JSONResponse{NotFoundJSONResponse: genapi.NotFoundJSONResponse(body)}, nil
			default:
				h.deps.Logger.WithContext(ctx).Warn("read events: mapped sentinel has no response variant — returning 500",
					logging.IntField("http.status", m.Status),
					logging.StringField("error.code", m.Code),
					logging.ErrorField(err),
				)
			}
		}
		if !isContextError(err) {
			h.deps.Logger.WithContext(ctx).Error("read events failed", logging.ErrorField(err))
		}
		return nil, err
	}

	out := make([]genapi.Event, 0, len(events))
	for _, ev := range events {
		var data map[string]any
		if len(ev.Data) > 0 {
			if err := json.Unmarshal(ev.Data, &data); err != nil {
				h.deps.Logger.WithContext(ctx).Error("read events: failed to decode stored event data",
					obs.EventType(ev.Type),
					logging.ErrorField(err),
				)
				return nil, fmt.Errorf("decode event %d data: %w", ev.ID, err)
			}
		} else {
			data = map[string]any{}
		}
		out = append(out, genapi.Event{
			Id:        ev.ID,
			Type:      ev.Type,
			Priority:  ev.Priority,
			Data:      data,
			CreatedAt: ev.CreatedAt,
		})
	}

	h.deps.Logger.WithContext(ctx).Debug("events read", logging.IntField("events.read.returned", len(out)))

	return genapi.ReadEvents200JSONResponse(genapi.ReadEventsResult{Events: out}), nil
}
