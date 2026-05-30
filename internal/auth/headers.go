// Copyright 2026 The CALM Authors
// SPDX-License-Identifier: Apache-2.0

package auth

// Wire-protocol constants for CALM's auth headers. Single source of truth
// shared by the server-side middleware, the calm-adapter binary, and the
// integration test helpers. Names match the OpenAPI security-scheme
// definitions in docs/api/openapi.yaml.
const (
	// HeaderAPIKey carries the namespace API key on every request (except
	// operational endpoints that bypass auth).
	HeaderAPIKey = "X-CALM-API-Key" //nolint:gosec // G101 false positive: header name, not a credential value.

	// HeaderAuthorization carries the per-client bearer token in namespaces
	// configured with require_client_credentials=true.
	HeaderAuthorization = "Authorization"

	// BearerPrefix is the scheme token that precedes the client bearer token
	// in the Authorization header value.
	BearerPrefix = "Bearer "
)
