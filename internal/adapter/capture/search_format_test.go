// Copyright 2026 The CALM Authors
// SPDX-License-Identifier: Apache-2.0

package capture

import (
	"strings"
	"testing"

	"github.com/one-harsh/calm/internal/adapter/calm"
)

func TestFormatSearchResults_BoundsLength(t *testing.T) {
	huge := strings.Repeat("x", maxSearchResultLen+1000)
	res := calm.SearchResults{Queries: []calm.QueryResult{{
		Query: "q",
		Hits:  []calm.Hit{{Title: "t", Snippet: huge, Source: "s", MatchLayer: "primary"}},
	}}}
	out := FormatSearchResults(res, "", SearchVocab{})

	if !strings.HasSuffix(out, "…") {
		t.Errorf("over-length result should end with an ellipsis")
	}
	if len(out) > maxSearchResultLen+len("…") {
		t.Errorf("result length = %d; want bounded to %d", len(out), maxSearchResultLen+len("…"))
	}
}

func TestFormatDocumentOrder_RendersVocabStrings(t *testing.T) {
	next := 3
	res := calm.SearchResults{
		Queries:    []calm.QueryResult{{Query: "", Hits: []calm.Hit{{Title: "l#1", Snippet: "body", Source: "l", MatchLayer: "document", Truncated: true}}}},
		NextOffset: &next,
	}
	shellA := SearchVocab{TruncatedMarker: "MARKER-A", ContinuationLine: "CONT-A "}
	shellB := SearchVocab{TruncatedMarker: "MARKER-B", ContinuationLine: "CONT-B "}

	a := FormatDocumentOrder(res, 0, "", shellA)
	if !strings.Contains(a, "MARKER-A") || !strings.Contains(a, "CONT-A 3") {
		t.Errorf("shell A vocab not rendered:\n%s", a)
	}
	b := FormatDocumentOrder(res, 0, "", shellB)
	if !strings.Contains(b, "MARKER-B") || !strings.Contains(b, "CONT-B 3") {
		t.Errorf("shell B vocab not rendered:\n%s", b)
	}
}

func TestSearchFormatters_ZeroHit(t *testing.T) {
	empty := calm.SearchResults{CorrelationID: "corr-z"}
	withPrefix := SearchVocab{ZeroHitRanked: "no matches", ZeroHitDocument: "no chunks at this offset", FeedbackPrefix: "fb "}

	if got := FormatSearchResults(empty, "src", withPrefix); got != "no matches under source=src\nfb corr-z\n" {
		t.Errorf("zero-hit ranked with a ref: %q", got)
	}
	if got := FormatDocumentOrder(empty, 0, "", withPrefix); got != "no chunks at this offset\nfb corr-z\n" {
		t.Errorf("zero-hit document with a ref: %q", got)
	}
	// Empty prefix → no feedback line and no trailing newline (shell byte parity).
	noPrefix := SearchVocab{ZeroHitRanked: "no matches"}
	if got := FormatSearchResults(empty, "", noPrefix); got != "no matches" {
		t.Errorf("zero-hit no-prefix must stay a bare message: %q", got)
	}
}

func TestSearchFormatters_FeedbackTrailer(t *testing.T) {
	res := calm.SearchResults{
		Queries:       []calm.QueryResult{{Query: "q", Hits: []calm.Hit{{Title: "t", Snippet: "s", Source: "src", MatchLayer: "primary"}}}},
		CorrelationID: "corr-1",
	}
	next := 1
	docRes := calm.SearchResults{
		Queries:       []calm.QueryResult{{Query: "", Hits: []calm.Hit{{Title: "l#1", Snippet: "body", Source: "l", MatchLayer: "document"}}}},
		NextOffset:    &next,
		CorrelationID: "corr-1",
	}
	withPrefix := SearchVocab{FeedbackPrefix: "fb "}

	for name, out := range map[string]string{
		"ranked":   FormatSearchResults(res, "", withPrefix),
		"document": FormatDocumentOrder(docRes, 0, "", withPrefix),
	} {
		if !strings.HasSuffix(out, "fb corr-1\n") {
			t.Errorf("%s: prefix + correlation id must append a trailing feedback line; got:\n%s", name, out)
		}
	}

	// An empty prefix renders no trailer — the MCP shell's output stays unchanged.
	if got := FormatSearchResults(res, "", SearchVocab{}); strings.Contains(got, "corr-1") {
		t.Errorf("empty feedback prefix must render no trailer; got:\n%s", got)
	}
	// A prefix with no correlation id renders no trailer.
	res.CorrelationID = ""
	if got := FormatSearchResults(res, "", withPrefix); strings.Contains(got, "fb ") {
		t.Errorf("absent correlation id must render no trailer; got:\n%s", got)
	}
}
