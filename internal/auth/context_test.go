// Copyright 2026 The CALM Authors
// SPDX-License-Identifier: Apache-2.0

package auth

import (
	"context"
	"testing"
)

func TestWithNamespace_ThenNamespaceFromContextReturnsValue(t *testing.T) {
	ctx := WithNamespace(context.Background(), "ns-a")
	if got := NamespaceFromContext(ctx); got != "ns-a" {
		t.Errorf("NamespaceFromContext = %q; want %q", got, "ns-a")
	}
}

func TestNamespaceFromContext_UnboundReturnsEmpty(t *testing.T) {
	if got := NamespaceFromContext(context.Background()); got != "" {
		t.Errorf("NamespaceFromContext on bare ctx = %q; want empty", got)
	}
}

func TestNamespaceFromContext_NonStringValueReturnsEmpty(t *testing.T) {
	// Direct context.WithValue using the same key type but storing a non-string
	// — exercises the type-assertion guard. Synthetic case (real callers go
	// through WithNamespace) but pins the safety branch.
	ctx := context.WithValue(context.Background(), namespaceCtxKey{}, 42)
	if got := NamespaceFromContext(ctx); got != "" {
		t.Errorf("NamespaceFromContext with int value = %q; want empty", got)
	}
}

func TestWithNamespace_ChildOverridesParent(t *testing.T) {
	parent := WithNamespace(context.Background(), "ns-a")
	child := WithNamespace(parent, "ns-b")
	if got := NamespaceFromContext(child); got != "ns-b" {
		t.Errorf("child override: NamespaceFromContext = %q; want %q", got, "ns-b")
	}
	// Parent is unchanged.
	if got := NamespaceFromContext(parent); got != "ns-a" {
		t.Errorf("parent after child shadow: NamespaceFromContext = %q; want %q", got, "ns-a")
	}
}
