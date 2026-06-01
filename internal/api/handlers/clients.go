// Copyright 2026 The CALM Authors
// SPDX-License-Identifier: Apache-2.0

package handlers

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	logging "github.com/one-harsh/context-logging"

	"github.com/one-harsh/calm/internal/api/genapi"
	"github.com/one-harsh/calm/internal/auth"
	"github.com/one-harsh/calm/internal/db"
	"github.com/one-harsh/calm/internal/obs"
)

func (h *Handlers) RegisterClient(
	ctx context.Context,
	request genapi.RegisterClientRequestObject,
) (genapi.RegisterClientResponseObject, error) {
	namespace := auth.NamespaceFromContext(ctx)
	if namespace == "" {
		h.deps.Logger.WithContext(ctx).Error("namespace missing from request context — auth middleware did not run")
		return nil, errors.New("namespace not present in request context")
	}

	name := string(request.Name)
	ctx = logging.Bind(ctx, obs.Client(name))

	if h.deps.Registry.RequiresClientCredentials(namespace) {
		return h.registerCredentialedClient(ctx, namespace, name)
	}
	return h.registerUncredentialedClient(ctx, namespace, name)
}

func (h *Handlers) registerUncredentialedClient(ctx context.Context, namespace, name string) (genapi.RegisterClientResponseObject, error) {
	result, err := h.deps.Clients.Register(ctx, namespace, name)
	if err != nil {
		if resp, ok := mapRegisterClientError(err, namespace, name); ok {
			return resp, nil
		}
		if !isContextError(err) {
			h.deps.Logger.WithContext(ctx).Error("register client failed", logging.ErrorField(err))
		}
		return nil, err
	}
	if !result.Created {
		detail := fmt.Sprintf("client %q already registered in namespace %q", name, namespace)
		return genapi.RegisterClient409JSONResponse{ConflictJSONResponse: genapi.ConflictJSONResponse{
			Error: "client_exists", Detail: &detail,
		}}, nil
	}
	return genapi.RegisterClient201JSONResponse{
		Name:      result.Name,
		Namespace: result.Namespace,
		CreatedAt: result.CreatedAt,
	}, nil
}

func (h *Handlers) registerCredentialedClient(ctx context.Context, namespace, name string) (genapi.RegisterClientResponseObject, error) {
	result, err := h.deps.Clients.RegisterWithCredential(ctx, namespace, name)
	if err != nil {
		if resp, ok := mapRegisterClientError(err, namespace, name); ok {
			return resp, nil
		}
		if !isContextError(err) {
			h.deps.Logger.WithContext(ctx).Error("register credentialed client failed", logging.ErrorField(err))
		}
		return nil, err
	}
	token := result.RawToken
	return genapi.RegisterClient201JSONResponse{
		Name:        result.Name,
		Namespace:   result.Namespace,
		CreatedAt:   result.CreatedAt,
		ClientToken: &token,
	}, nil
}

func mapRegisterClientError(err error, namespace, name string) (genapi.RegisterClientResponseObject, bool) {
	m, ok := mapClientError(err)
	if !ok {
		return nil, false
	}
	switch m.Status {
	case http.StatusConflict:
		detail := fmt.Sprintf("client %q already registered in namespace %q", name, namespace)
		return genapi.RegisterClient409JSONResponse{ConflictJSONResponse: genapi.ConflictJSONResponse{
			Error: m.Code, Detail: &detail,
		}}, true
	case http.StatusBadRequest:
		return genapi.RegisterClient400JSONResponse{BadRequestJSONResponse: genapi.BadRequestJSONResponse{
			Error: m.Code, Detail: &m.Detail,
		}}, true
	}
	return nil, false
}

func (h *Handlers) RotateClientToken(
	ctx context.Context,
	request genapi.RotateClientTokenRequestObject,
) (genapi.RotateClientTokenResponseObject, error) {
	namespace := auth.NamespaceFromContext(ctx)
	if namespace == "" {
		h.deps.Logger.WithContext(ctx).Error("namespace missing from request context — auth middleware did not run")
		return nil, errors.New("namespace not present in request context")
	}

	name := string(request.Name)
	ctx = logging.Bind(ctx, obs.Client(name))

	// Rotation requires the caller to have authenticated as the client
	// they're rotating. Three rejection cases all collapse to 401:
	//   - uncredentialed namespace: auth middleware stamped no client, so
	//     authClient == "" (no token to rotate; no authority to rotate one).
	//   - credentialed namespace, caller holds a different client's token:
	//     authClient != name (client-A can't rotate client-B's credentials).
	//   - credentialed namespace, no Authorization header: auth middleware
	//     already 401'd; this handler never ran. Defensive only.
	authClient := auth.ClientFromContext(ctx)
	if authClient == "" || authClient != name {
		return genapi.RotateClientToken401JSONResponse{UnauthorizedJSONResponse: genapi.UnauthorizedJSONResponse{
			Error: "unauthorized",
		}}, nil
	}

	newToken, err := h.deps.Clients.RotateToken(ctx, namespace, name)
	if err != nil {
		if errors.Is(err, db.ErrClientNotFound) {
			detail := fmt.Sprintf("client %q not found in namespace %q", name, namespace)
			return genapi.RotateClientToken404JSONResponse{NotFoundJSONResponse: genapi.NotFoundJSONResponse{
				Error: "client_not_found", Detail: &detail,
			}}, nil
		}
		if !isContextError(err) {
			h.deps.Logger.WithContext(ctx).Error("rotate client token failed", logging.ErrorField(err))
		}
		return nil, err
	}

	return genapi.RotateClientToken200JSONResponse{
		Name:        name,
		Namespace:   namespace,
		ClientToken: newToken,
		RotatedAt:   time.Now().UTC(),
	}, nil
}
