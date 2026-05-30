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

func TestWithClient_ThenClientFromContextReturnsValue(t *testing.T) {
	ctx := WithClient(context.Background(), "factory-pipeline")
	if got := ClientFromContext(ctx); got != "factory-pipeline" {
		t.Errorf("ClientFromContext = %q; want factory-pipeline", got)
	}
}

func TestClientFromContext_UnboundReturnsEmpty(t *testing.T) {
	// Handlers rely on "" meaning "no client token authenticated" — fall
	// back to body-supplied `client` field. Pin that contract.
	if got := ClientFromContext(context.Background()); got != "" {
		t.Errorf("ClientFromContext on bare ctx = %q; want empty", got)
	}
}

func TestClientFromContext_NonStringValueReturnsEmpty(t *testing.T) {
	ctx := context.WithValue(context.Background(), clientCtxKey{}, 42)
	if got := ClientFromContext(ctx); got != "" {
		t.Errorf("ClientFromContext with int value = %q; want empty", got)
	}
}

func TestWithClient_NamespaceAndClientCoexist(t *testing.T) {
	// Auth middleware stamps both. Reading one mustn't disturb the other.
	ctx := WithNamespace(context.Background(), "ns-a")
	ctx = WithClient(ctx, "alice")
	if got := NamespaceFromContext(ctx); got != "ns-a" {
		t.Errorf("namespace = %q; want ns-a", got)
	}
	if got := ClientFromContext(ctx); got != "alice" {
		t.Errorf("client = %q; want alice", got)
	}
}
