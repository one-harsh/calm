// Copyright 2026 The CALM Authors
// SPDX-License-Identifier: Apache-2.0

package integration

import (
	"context"
	"testing"

	"github.com/one-harsh/calm/internal/db"
)

// Independent leak probe: two sessions in one namespace. Session A indexes a
// source; session B issues a source-scoped search for A's content and must see
// nothing. This exercises session isolation on the source-scoped search path —
// the path the leak reaches and that the existing suite never covered (its
// session-isolation searches all run unscoped, source == "").
func TestT5Oracle_SourceScopedSessionLeak(t *testing.T) {
	t.Parallel()
	store, sqlDB, teardown := openConcreteStore(t)
	defer teardown()
	seedClient(t, sqlDB, "ns-a", db.DefaultClient)
	sessA := seedSession(t, sqlDB, "ns-a", db.DefaultClient, 60)
	sessB := seedSession(t, sqlDB, "ns-a", db.DefaultClient, 60)
	seedIndexedSource(t, store, "ns-a", sessA.ID, "shared.go", []db.Chunk{
		{Title: "secret", Content: "the alphasecret token lives here", ContentType: "prose"},
	})

	got, err := store.Sources().Search(context.Background(), "ns-a", db.SearchInput{
		SessionID: sessB.ID, Query: "alphasecret", Source: "shared.go",
	})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("session B sees %+v via source-scoped search; want none (session isolation)", got)
	}
}
