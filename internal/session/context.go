// Copyright 2026 The CALM Authors
// SPDX-License-Identifier: Apache-2.0

package session

import "context"

type metadataCtxKey struct{}

func WithMetadata(ctx context.Context, md SessionMetadata) context.Context {
	return context.WithValue(ctx, metadataCtxKey{}, md)
}

// MetadataFromContext returns ok=false when the middleware hasn't run.
func MetadataFromContext(ctx context.Context) (SessionMetadata, bool) {
	md, ok := ctx.Value(metadataCtxKey{}).(SessionMetadata)
	return md, ok
}
