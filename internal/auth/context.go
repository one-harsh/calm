// Copyright 2026 The CALM Authors
// SPDX-License-Identifier: Apache-2.0

package auth

import "context"

// namespaceCtxKey is intentionally separate from the logging library's
// field bag — namespace is a business-logic trust-scope value and must
// not depend on an observability library to propagate.
type namespaceCtxKey struct{}

// clientCtxKey carries the auth-resolved client name in namespaces that
// require client credentials (where the workload presented a client token).
// When credentials aren't required this remains unset; handlers fall back
// to the body-supplied `client` field.
type clientCtxKey struct{}

func WithNamespace(ctx context.Context, ns string) context.Context {
	return context.WithValue(ctx, namespaceCtxKey{}, ns)
}

// NamespaceFromContext returns "" when the middleware hasn't run.
func NamespaceFromContext(ctx context.Context) string {
	if v, ok := ctx.Value(namespaceCtxKey{}).(string); ok {
		return v
	}
	return ""
}

func WithClient(ctx context.Context, name string) context.Context {
	return context.WithValue(ctx, clientCtxKey{}, name)
}

// ClientFromContext returns "" when the middleware didn't authenticate a
// client token — either because the namespace doesn't require client
// credentials, or because the request is on a pre-auth path. Handlers must
// distinguish "" from a valid client name.
func ClientFromContext(ctx context.Context) string {
	if v, ok := ctx.Value(clientCtxKey{}).(string); ok {
		return v
	}
	return ""
}
