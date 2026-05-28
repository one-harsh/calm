// Copyright 2026 The CALM Authors
// SPDX-License-Identifier: Apache-2.0

package auth

import "context"

// namespaceCtxKey is intentionally separate from the logging library's
// field bag — namespace is a business-logic trust-scope value and must
// not depend on an observability library to propagate.
type namespaceCtxKey struct{}

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
