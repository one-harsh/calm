// Copyright 2026 The CALM Authors
// SPDX-License-Identifier: Apache-2.0

package mcp

import (
	"strings"
	"testing"

	"github.com/one-harsh/calm/internal/adapter/calm"
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
	out := formatSearchResults(res)

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

func TestFormatSearchResults_BoundsLength(t *testing.T) {
	huge := strings.Repeat("x", maxSearchResultLen+1000)
	res := calm.SearchResults{Queries: []calm.QueryResult{{
		Query: "q",
		Hits:  []calm.Hit{{Title: "t", Snippet: huge, Source: "s", MatchLayer: "primary"}},
	}}}
	out := formatSearchResults(res)

	if !strings.HasSuffix(out, "…") {
		t.Errorf("over-length result should end with an ellipsis")
	}
	if len(out) > maxSearchResultLen+len("…") {
		t.Errorf("result length = %d; want bounded to %d", len(out), maxSearchResultLen+len("…"))
	}
}
