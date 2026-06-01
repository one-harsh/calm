// Copyright 2026 The CALM Authors
// SPDX-License-Identifier: Apache-2.0

package auth

import "context"

type namespaceCtxKey struct{}

// clientCtxKey is unset in uncredentialed namespaces; handlers fall back to body.client there.
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
