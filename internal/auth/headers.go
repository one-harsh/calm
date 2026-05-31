// Copyright 2026 The CALM Authors
// SPDX-License-Identifier: Apache-2.0

package auth

// Wire-protocol header names — match docs/api/openapi.yaml.
const (
	HeaderAPIKey         = "X-CALM-API-Key" //nolint:gosec // G101 false positive: header name, not a credential value.
	HeaderAuthorization  = "Authorization"
	BearerPrefix         = "Bearer "
	HeaderSessionToken   = "X-CALM-Session-Token" //nolint:gosec // G101 false positive: header name, not a credential value.
	HeaderIdempotencyKey = "Idempotency-Key"
)
