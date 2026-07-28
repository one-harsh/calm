// Copyright 2026 The CALM Authors
// SPDX-License-Identifier: Apache-2.0

package capture

import (
	"fmt"
	"strings"

	"github.com/one-harsh/calm/internal/adapter/calm"
)

const maxSearchResultLen = 8192

// SearchVocab is the per-shell vocabulary the shared formatters render with, so
// byte-for-byte output stays each shell's own while the layout lives in one
// place. An empty FeedbackPrefix renders no feedback-ref line — the shell that
// leaves it empty surfaces the ref through its own outcome affordance instead.
type SearchVocab struct {
	TruncatedMarker  string
	ContinuationLine string
	FeedbackPrefix   string
	ZeroHitRanked    string
	ZeroHitDocument  string
}

func totalHits(res calm.SearchResults) int {
	n := 0
	for _, q := range res.Queries {
		n += len(q.Hits)
	}
	return n
}

func sourceNote(source string) string {
	if source == "" {
		return ""
	}
	return " under source=" + source
}

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}

func FormatSearchResults(res calm.SearchResults, source string, v SearchVocab) string {
	if totalHits(res) == 0 {
		return withFeedbackLine(v.ZeroHitRanked+sourceNote(source), v, res.CorrelationID)
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%d %s across %d %s:\n",
		totalHits(res), plural(totalHits(res), "hit", "hits"),
		len(res.Queries), plural(len(res.Queries), "query", "queries"))
	for _, q := range res.Queries {
		fmt.Fprintf(&b, "\n# %q — %d %s\n", q.Query, len(q.Hits), plural(len(q.Hits), "hit", "hits"))
		for _, h := range q.Hits {
			fmt.Fprintf(&b, "[%s] %s  (%s)\n%s\n", h.MatchLayer, h.Title, h.Source, h.Snippet)
		}
	}
	out := b.String()
	if len(out) > maxSearchResultLen {
		out = strings.ToValidUTF8(out[:maxSearchResultLen], "") + "…"
	}
	return withFeedbackLine(out, v, res.CorrelationID)
}

func FormatDocumentOrder(res calm.SearchResults, offset int, source string, v SearchVocab) string {
	if totalHits(res) == 0 {
		return withFeedbackLine(v.ZeroHitDocument+sourceNote(source), v, res.CorrelationID)
	}
	var hits []calm.Hit
	for _, q := range res.Queries {
		hits = append(hits, q.Hits...)
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%d %s in document order from offset %d:\n",
		len(hits), plural(len(hits), "chunk", "chunks"), offset)
	for _, h := range hits {
		fmt.Fprintf(&b, "\n## %s\n%s\n", h.Title, h.Snippet)
		if h.Truncated {
			b.WriteString(v.TruncatedMarker)
			b.WriteByte('\n')
		}
	}
	if res.NextOffset != nil {
		fmt.Fprintf(&b, "\n%s%d\n", v.ContinuationLine, *res.NextOffset)
	}
	return withFeedbackLine(b.String(), v, res.CorrelationID)
}

// withFeedbackLine is the single place the search presentation threads the
// feedback ref, for both shells.
func withFeedbackLine(out string, v SearchVocab, ref string) string {
	if v.FeedbackPrefix == "" || ref == "" {
		return out
	}
	if out != "" && !strings.HasSuffix(out, "\n") {
		out += "\n"
	}
	return out + v.FeedbackPrefix + ref + "\n"
}
