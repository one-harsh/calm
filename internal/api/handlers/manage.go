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
	"github.com/one-harsh/calm/internal/obs"
)

func (h *Handlers) ManageListSessions(
	ctx context.Context,
	request genapi.ManageListSessionsRequestObject,
) (genapi.ManageListSessionsResponseObject, error) {
	namespace := auth.NamespaceFromContext(ctx)
	if namespace == "" {
		h.deps.Logger.WithContext(ctx).Error("namespace missing from request context — auth middleware did not run")
		return nil, errors.New("namespace not present in request context")
	}

	sessions, err := h.deps.Sessions.List(ctx, manageSessionsFilter(namespace, request.Params.Client, request.Params.Labels))
	if err != nil {
		if !isContextError(err) {
			h.deps.Logger.WithContext(ctx).Error("manage list sessions failed", logging.ErrorField(err))
		}
		return nil, err
	}

	out := make([]genapi.ManagedSession, len(sessions))
	for i, s := range sessions {
		out[i] = toManagedSession(s)
	}
	h.deps.Logger.WithContext(ctx).Debug("manage list sessions", logging.IntField("sessions.listed", len(out)))
	return genapi.ManageListSessions200JSONResponse(genapi.ListManagedSessionsResult{Sessions: out}), nil
}

func (h *Handlers) ManageDeleteSessions(
	ctx context.Context,
	request genapi.ManageDeleteSessionsRequestObject,
) (genapi.ManageDeleteSessionsResponseObject, error) {
	namespace := auth.NamespaceFromContext(ctx)
	if namespace == "" {
		h.deps.Logger.WithContext(ctx).Error("namespace missing from request context — auth middleware did not run")
		return nil, errors.New("namespace not present in request context")
	}

	filter := manageSessionsFilter(namespace, request.Params.Client, request.Params.Labels)
	if filter.Client != "" {
		ctx = logging.Bind(ctx, obs.Client(filter.Client))
	}

	if request.Params.DryRun != nil && *request.Params.DryRun {
		affected, err := h.deps.Sessions.Count(ctx, filter)
		if err != nil {
			if !isContextError(err) {
				h.deps.Logger.WithContext(ctx).Error("manage delete sessions dry-run failed", logging.ErrorField(err))
			}
			return nil, err
		}
		h.deps.Logger.WithContext(ctx).Debug("manage delete sessions dry-run", logging.IntField("sessions.affected", affected))
		return genapi.ManageDeleteSessions200JSONResponse(genapi.DeleteManagedSessionsResult{AffectedSessions: &affected}), nil
	}

	result, err := h.deps.Sessions.DeleteAll(ctx, filter)
	if err != nil {
		if !isContextError(err) {
			h.deps.Logger.WithContext(ctx).Error("manage delete sessions failed", logging.ErrorField(err))
		}
		return nil, err
	}

	h.deps.Logger.WithContext(ctx).WithAuditEvent(logging.ResourceDelete).Info(
		"sessions bulk-deleted",
		obs.AuditInitiatorAPI,
		logging.IntField("sessions.deleted", result.DeletedSessions),
		obs.CascadedEvents(result.Cascaded.Events),
		obs.CascadedSources(result.Cascaded.Sources),
		obs.CascadedChunks(result.Cascaded.Chunks),
		obs.CascadedLabels(result.Cascaded.Labels),
	)

	cascaded := toCascadeCounts(result.Cascaded)
	return genapi.ManageDeleteSessions200JSONResponse(genapi.DeleteManagedSessionsResult{
		DeletedSessions: &result.DeletedSessions,
		Cascaded:        &cascaded,
	}), nil
}

func (h *Handlers) ManageListClients(
	ctx context.Context,
	_ genapi.ManageListClientsRequestObject,
) (genapi.ManageListClientsResponseObject, error) {
	namespace := auth.NamespaceFromContext(ctx)
	if namespace == "" {
		h.deps.Logger.WithContext(ctx).Error("namespace missing from request context — auth middleware did not run")
		return nil, errors.New("namespace not present in request context")
	}

	clients, err := h.deps.Clients.List(ctx, namespace)
	if err != nil {
		if !isContextError(err) {
			h.deps.Logger.WithContext(ctx).Error("manage list clients failed", logging.ErrorField(err))
		}
		return nil, err
	}

	out := make([]genapi.ClientSummary, len(clients))
	for i, c := range clients {
		out[i] = genapi.ClientSummary{
			Name:         c.Name,
			SessionCount: c.SessionCount,
			LastActivity: c.LastActivity,
		}
	}
	h.deps.Logger.WithContext(ctx).Debug("manage list clients", logging.IntField("clients.listed", len(out)))
	return genapi.ManageListClients200JSONResponse(genapi.ListClientsResult{Clients: out}), nil
}

func (h *Handlers) ManageDeleteClient(
	ctx context.Context,
	request genapi.ManageDeleteClientRequestObject,
) (genapi.ManageDeleteClientResponseObject, error) {
	namespace := auth.NamespaceFromContext(ctx)
	if namespace == "" {
		h.deps.Logger.WithContext(ctx).Error("namespace missing from request context — auth middleware did not run")
		return nil, errors.New("namespace not present in request context")
	}

	name := request.Client
	ctx = logging.Bind(ctx, obs.Client(name))

	if request.Params.DryRun != nil && *request.Params.DryRun {
		affected, err := h.deps.Clients.PreviewDelete(ctx, namespace, name)
		if err != nil {
			if resp, ok := mapManageDeleteClientError(err); ok {
				return resp, nil
			}
			if !isContextError(err) {
				h.deps.Logger.WithContext(ctx).Error("manage delete client dry-run failed", logging.ErrorField(err))
			}
			return nil, err
		}
		h.deps.Logger.WithContext(ctx).Debug("manage delete client dry-run", logging.IntField("sessions.affected", affected))
		return genapi.ManageDeleteClient200JSONResponse(genapi.DeleteClientResult{AffectedSessions: &affected}), nil
	}

	result, err := h.deps.Clients.Delete(ctx, namespace, name)
	if err != nil {
		if resp, ok := mapManageDeleteClientError(err); ok {
			return resp, nil
		}
		if !isContextError(err) {
			h.deps.Logger.WithContext(ctx).Error("manage delete client failed", logging.ErrorField(err))
		}
		return nil, err
	}

	// The cascade drops the client's sessions through the clients FK, outside the
	// session service's own delete paths — evict so a cascaded token stops
	// resolving out of the session cache once its row is gone.
	h.deps.Sessions.InvalidateNamespaceCache(namespace)

	h.deps.Logger.WithContext(ctx).WithAuditEvent(logging.ResourceDelete).Info(
		"client deleted",
		obs.AuditInitiatorAPI,
		logging.IntField("sessions.deleted", result.DeletedSessions),
		obs.CascadedEvents(result.Cascaded.Events),
		obs.CascadedSources(result.Cascaded.Sources),
		obs.CascadedChunks(result.Cascaded.Chunks),
		obs.CascadedLabels(result.Cascaded.Labels),
	)

	cascaded := toCascadeCounts(result.Cascaded)
	return genapi.ManageDeleteClient200JSONResponse(genapi.DeleteClientResult{
		DeletedClient:   &result.Client,
		DeletedSessions: &result.DeletedSessions,
		Cascaded:        &cascaded,
	}), nil
}

// manageSessionsFilter stamps the namespace the API key authenticated into — the
// caller never supplies it — and layers the optional client and label filters on top.
func manageSessionsFilter(namespace string, client *string, labels *map[string]string) db.ListSessionsFilter {
	filter := db.ListSessionsFilter{Namespace: namespace}
	if client != nil {
		filter.Client = *client
	}
	if labels != nil {
		filter.Labels = *labels
	}
	return filter
}

func toManagedSession(s db.ManagedSession) genapi.ManagedSession {
	ms := genapi.ManagedSession{
		Namespace:    s.Namespace,
		Client:       s.Client,
		TtlMinutes:   s.TTLMinutes,
		CreatedAt:    s.CreatedAt,
		LastActivity: s.LastActivity,
		EventCount:   s.EventCount,
	}
	if len(s.Labels) > 0 {
		ms.Labels = &s.Labels
	}
	return ms
}

func toCascadeCounts(c db.CascadeCounts) genapi.CascadeCounts {
	return genapi.CascadeCounts{
		Sources: c.Sources,
		Chunks:  c.Chunks,
		Events:  c.Events,
		Labels:  c.Labels,
	}
}

func mapManageDeleteClientError(err error) (genapi.ManageDeleteClientResponseObject, bool) {
	m, ok := mapClientError(err)
	if !ok {
		return nil, false
	}
	switch m.Status {
	case http.StatusConflict:
		return genapi.ManageDeleteClient409JSONResponse{ConflictJSONResponse: genapi.ConflictJSONResponse{
			Error: m.Code, Detail: &m.Detail,
		}}, true
	case http.StatusNotFound:
		return genapi.ManageDeleteClient404JSONResponse{NotFoundJSONResponse: genapi.NotFoundJSONResponse{
			Error: m.Code, Detail: &m.Detail,
		}}, true
	}
	return nil, false
}
