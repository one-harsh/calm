// Copyright 2026 The CALM Authors
// SPDX-License-Identifier: Apache-2.0

package capture

import (
	"context"
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
