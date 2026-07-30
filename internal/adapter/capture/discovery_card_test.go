// Copyright 2026 The CALM Authors
// SPDX-License-Identifier: Apache-2.0

package capture

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/mock"

	logging "github.com/one-harsh/context-logging"

	"github.com/one-harsh/calm/internal/adapter/calm"
)

const testCardMarker = "CALM keeps captured output searchable"

// The retrieval-discovery card (opt-in via WithDiscoveryCard) rides the
// session's first captured presentation once and points at the shell's own
// recall command; later captures carry no card, and the MCP shell (no opt-in)
// carries none at all.
func TestDiscoveryCard_FirstCaptureOnly(t *testing.T) {
	m := calm.NewMockClient(t)
	m.EXPECT().Ingest(mock.Anything, "tok-1", mock.Anything).
		Return(calm.IngestSummary{Source: "calm:v1:vcs:git:status", SectionsIndexed: 1, SectionsTotal: 1}, nil)
	sess := &stubSession{reg: NewRegistry(), token: "tok-1"}
	e := NewEngine(m, sess, &stubSink{}, logging.Nop(), "calm-capture search", WithDiscoveryCard())

	// "hi" (no trailing newline) exercises the card's separator normalization.
	first := e.Capture(context.Background(), Spec{Ingest: "hi", Visible: "hi", Plan: planFor("git status", "hi")})
	second := e.Capture(context.Background(), Spec{Ingest: "hi", Visible: "hi", Plan: planFor("git status", "hi")})

	if !strings.Contains(first.Visible, testCardMarker) {
		t.Errorf("first capture must carry the discovery card; got:\n%s", first.Visible)
	}
	if !strings.Contains(first.Visible, "calm-capture search source=") {
		t.Errorf("card must point at the shell's recall command; got:\n%s", first.Visible)
	}
	if strings.Contains(second.Visible, testCardMarker) {
		t.Errorf("second capture must not carry the discovery card; got:\n%s", second.Visible)
	}
}

// Without the opt-in the engine appends no card — the MCP shell's guarantee of
// zero presentation change.
func TestDiscoveryCard_OffByDefault(t *testing.T) {
	m := calm.NewMockClient(t)
	m.EXPECT().Ingest(mock.Anything, "tok-1", mock.Anything).
		Return(calm.IngestSummary{Source: "calm:v1:vcs:git:status", SectionsIndexed: 1, SectionsTotal: 1}, nil)
	sess := &stubSession{reg: NewRegistry(), token: "tok-1"}
	e := NewEngine(m, sess, &stubSink{}, logging.Nop(), "calm_search")

	out := e.Capture(context.Background(), Spec{Ingest: "hi\n", Visible: "hi\n", Plan: planFor("git status", "hi\n")})

	if strings.Contains(out.Visible, testCardMarker) {
		t.Errorf("no card without WithDiscoveryCard; got:\n%s", out.Visible)
	}
}

// The session-start card leads with provenance, teaches recall, and — over an
// empty corpus — carries no inventory tail (never a stale one).
func TestSessionStartCard_EmptyCorpus_StaticOnly(t *testing.T) {
	card := SessionStartCard("calm-capture search", 0, nil)
	if !strings.Contains(card, "session-start hook") {
		t.Errorf("card must identify its emitter; got:\n%s", card)
	}
	if !strings.Contains(card, testCardMarker) || !strings.Contains(card, "calm-capture search source=") {
		t.Errorf("card must carry the recall affordance; got:\n%s", card)
	}
	if strings.Contains(card, "captured so far (retrieve") {
		t.Errorf("empty corpus must render no inventory; got:\n%s", card)
	}
}

// With a corpus the card states the capture count and lists the identities.
func TestSessionStartCard_WithCorpus_ListsIdentities(t *testing.T) {
	entries := []InventoryEntry{
		{Label: "calm:v1:file:read:a.go", Token: "abc123", Seq: 3},
		{Label: "calm:v1:shell:sh#2", Token: "def456", Seq: 2},
	}
	card := SessionStartCard("calm-capture search", 3, entries)
	if !strings.Contains(card, "3 captured so far") {
		t.Errorf("card must state the capture count; got:\n%s", card)
	}
	// The card emits the fused label (base@token), not the bare base — a bare
	// label would bypass the staleness validation the recall affordance promises.
	for _, e := range entries {
		fused := e.Label + "@" + e.Token
		if !strings.Contains(card, fused) {
			t.Errorf("card must list the fused identity %q; got:\n%s", fused, card)
		}
	}
}

// The refresher identifies its emitter and phrases itself to supersede any
// earlier inventory — the load-bearing dedup property over a summarized context.
func TestSessionRefresherCard_Supersedes(t *testing.T) {
	card := SessionRefresherCard("calm-capture search", nil)
	if !strings.Contains(card, "session-start hook") || !strings.Contains(card, "replaces any earlier") {
		t.Errorf("refresher must identify and supersede; got:\n%s", card)
	}
}

// Beyond K identities, only the most-recent survive (entries arrive
// most-recent-first).
func TestRenderInventory_KOverflow_MostRecentSurvive(t *testing.T) {
	var entries []InventoryEntry
	total := inventoryMaxEntries + 5
	for i := 0; i < total; i++ {
		entries = append(entries, InventoryEntry{Label: fmt.Sprintf("id%02d", i), Seq: int64(total - i)})
	}
	got := renderInventory(entries)
	if !strings.Contains(got, "id00") {
		t.Errorf("most-recent identity must be listed; got:\n%s", got)
	}
	if overflow := fmt.Sprintf("id%02d", inventoryMaxEntries); strings.Contains(got, overflow) {
		t.Errorf("identity beyond K (%s) must be dropped; got:\n%s", overflow, got)
	}
}

// The byte cap cuts whole lines, so the most-recent identities survive intact
// and no partial label is emitted.
func TestRenderInventory_ByteCap_KeepsMostRecentWhole(t *testing.T) {
	var entries []InventoryEntry
	for i := 0; i < inventoryMaxEntries; i++ {
		entries = append(entries, InventoryEntry{
			Label: fmt.Sprintf("id%02d-", i) + strings.Repeat("x", 300),
			Seq:   int64(inventoryMaxEntries - i),
		})
	}
	got := renderInventory(entries)
	if len(got) > inventoryMaxBytes {
		t.Errorf("rendered %d bytes; want <= %d", len(got), inventoryMaxBytes)
	}
	if !strings.Contains(got, "id00-"+strings.Repeat("x", 300)) {
		t.Errorf("most-recent identity must survive whole; got:\n%s", got)
	}
	last := fmt.Sprintf("id%02d-", inventoryMaxEntries-1)
	if strings.Contains(got, last) {
		t.Errorf("least-recent identity (%s) must be dropped by the byte cap; got:\n%s", last, got)
	}
}
