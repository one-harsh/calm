// Copyright 2026 The CALM Authors
// SPDX-License-Identifier: Apache-2.0

package mcp

import (
	"strings"
	"testing"

	"github.com/one-harsh/calm/internal/adapter/calm"
	"github.com/one-harsh/calm/internal/adapter/capture"
)

func TestFormatSearchResults_RendersHitsPerQuery(t *testing.T) {
	res := calm.SearchResults{Queries: []calm.QueryResult{
		{Query: "alpha", Hits: []calm.Hit{
			{Title: "a.go", Snippet: "alpha snippet", Source: "calm:v1:file:read:a.go", MatchLayer: "primary"},
			{Title: "b.go", Snippet: "alpha again", Source: "calm:v1:file:read:b.go", MatchLayer: "trigram"},
		}},
		{Query: "beta", Hits: []calm.Hit{
			{Title: "c.go", Snippet: "beta snippet", Source: "calm:v1:file:read:c.go", MatchLayer: "primary"},
		}},
	}}
	out := capture.FormatSearchResults(res, "", searchVocab)

	for _, want := range []string{
		"3 hits across 2 queries",
		`# "alpha" — 2 hits`,
		`# "beta" — 1 hit`, // singular exercises plural()
		"[primary] a.go  (calm:v1:file:read:a.go)",
		"alpha snippet",
		"[trigram] b.go",
		"beta snippet",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
}

func TestFormatDocumentOrder_NoAnnotationsWithContinuation(t *testing.T) {
	next := 4
	res := calm.SearchResults{
		Queries: []calm.QueryResult{{Query: "", Hits: []calm.Hit{
			{Title: "log.txt#1", Snippet: "first chunk body", Source: "calm:v1:file:read:log.txt", MatchLayer: "document"},
			{Title: "log.txt#2", Snippet: "second chunk body", Source: "calm:v1:file:read:log.txt", MatchLayer: "document"},
		}}},
		NextOffset: &next,
	}
	out := capture.FormatDocumentOrder(res, 0, "", searchVocab)

	for _, want := range []string{
		"2 chunks in document order from offset 0:",
		"## log.txt#1",
		"first chunk body",
		"## log.txt#2",
		"second chunk body",
		"more chunks remain — call calm_search again with source and offset: 4",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
	// Document order carries no ranking annotations.
	for _, banned := range []string{"[document]", "[primary]", "[trigram]"} {
		if strings.Contains(out, banned) {
			t.Errorf("unexpected %q annotation in:\n%s", banned, out)
		}
	}
	// Chunk order is preserved.
	if strings.Index(out, "log.txt#1") > strings.Index(out, "log.txt#2") {
		t.Errorf("chunks rendered out of document order:\n%s", out)
	}
}

func TestFormatDocumentOrder_FinalPageOmitsContinuation(t *testing.T) {
	res := calm.SearchResults{
		Queries: []calm.QueryResult{{Query: "", Hits: []calm.Hit{
			{Title: "log.txt#9", Snippet: "last chunk body", Source: "calm:v1:file:read:log.txt", MatchLayer: "document"},
		}}},
		NextOffset: nil,
	}
	out := capture.FormatDocumentOrder(res, 8, "", searchVocab)

	if strings.Contains(out, "more chunks remain") {
		t.Errorf("final page must omit the continuation hint:\n%s", out)
	}
	if !strings.Contains(out, "from offset 8:") {
		t.Errorf("expected the requested offset in the header:\n%s", out)
	}
}

func TestFormatDocumentOrder_TruncatedMarker(t *testing.T) {
	next := 1
	res := calm.SearchResults{
		Queries: []calm.QueryResult{{Query: "", Hits: []calm.Hit{
			{Title: "log.txt#1", Snippet: "an exact-text prefix of a giant chunk", Source: "calm:v1:file:read:log.txt", MatchLayer: "document", Truncated: true},
		}}},
		NextOffset: &next,
	}
	out := capture.FormatDocumentOrder(res, 0, "", searchVocab)

	// Literal text pinned: the marker names the budget_bytes parameter the
	// tool exposes, so the advertised recovery is actionable.
	if !strings.Contains(out, "[truncated — raise budget_bytes or use a ranked query for the rest]") {
		t.Errorf("expected the truncated marker for a truncated first chunk:\n%s", out)
	}
	if !strings.Contains(out, "offset: 1") {
		t.Errorf("a truncated chunk still advances next_offset:\n%s", out)
	}
}

// The zero-hit paths route through the shared formatters now; the MCP shell's
// empty FeedbackPrefix must keep their bytes exactly as before — the bare
// message, no ref line, no trailing newline — even when a correlation id exists.
func TestFormatZeroHit_MCPBytesUnchanged(t *testing.T) {
	empty := calm.SearchResults{CorrelationID: "corr-x"}

	if got := capture.FormatSearchResults(empty, "calm:v1:x", searchVocab); got != "no matches under source=calm:v1:x" {
		t.Errorf("zero-hit ranked bytes drifted: %q", got)
	}
	if got := capture.FormatSearchResults(empty, "", searchVocab); got != "no matches" {
		t.Errorf("zero-hit ranked (no source) bytes drifted: %q", got)
	}
	if got := capture.FormatDocumentOrder(empty, 0, "calm:v1:x", searchVocab); got != "no chunks at this offset under source=calm:v1:x" {
		t.Errorf("zero-hit document bytes drifted: %q", got)
	}
}
