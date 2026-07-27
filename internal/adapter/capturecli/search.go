// Copyright 2026 The CALM Authors
// SPDX-License-Identifier: Apache-2.0

package capturecli

import (
	"context"
	"flag"
	"fmt"
	"strconv"
	"strings"

	"github.com/one-harsh/calm/internal/adapter/calm"
	"github.com/one-harsh/calm/internal/adapter/capture"
	"github.com/one-harsh/calm/internal/adapter/obs"
)

func (d Deps) searchCmd(ctx context.Context, args []string) int {
	fs := flag.NewFlagSet("search", flag.ContinueOnError)
	fs.SetOutput(d.Stderr)
	sessionID := fs.String("session", "", "harness conversation id")
	source := fs.String("source", "", "source label to scope retrieval to")
	offset := fs.Int("offset", 0, "document-order start offset")
	limit := fs.Int("limit", 0, "max hits per query / chunks per page")
	budget := fs.Int("budget-bytes", 0, "response byte budget")
	if err := fs.Parse(args); err != nil {
		return 2
	}

	queries := splitSearchArgs(fs.Args(), source, offset, limit, budget)
	if len(queries) == 0 && *source == "" {
		_, _ = fmt.Fprintln(d.Stderr, "calm-capture search: queries or source is required")
		return 2
	}
	if *offset < 0 {
		_, _ = fmt.Fprintln(d.Stderr, "calm-capture search: offset must be >= 0")
		return 2
	}
	documentOrder := len(queries) == 0

	mgr, err := d.manager(sessionIDOr(*sessionID))
	if err != nil {
		return d.degradedStderr(obs.DegradedReasonCaptureFailed)
	}
	view, err := mgr.View(ctx)
	if err != nil {
		return d.degradedStderr(obs.DegradedReasonCaptureFailed)
	}
	if view.AuthFailed {
		return d.degradedStderr(obs.DegradedReasonAuthFailed)
	}
	if view.Token == "" {
		return d.degradedStderr(obs.DegradedReasonCalmUnreachable)
	}

	// Strip and validate the fused staleness suffix locally before forwarding —
	// a stale token must resolve as session_lost, not reach CALM and return
	// calm_unreachable. Base-only labels pass through.
	calmSource := *source
	if *source != "" {
		stripped, ok := view.Registry.ValidateAndStrip(*source)
		if !ok {
			return d.degradedStderr(obs.DegradedReasonSessionLost)
		}
		calmSource = stripped
	}

	in := calm.SearchInput{Queries: queries, Source: calmSource, Limit: *limit, BudgetBytes: *budget}
	if documentOrder {
		in.Offset = *offset
	}
	sctx, cancel := context.WithTimeout(ctx, opTimeout)
	defer cancel()
	res, serr := d.Client.Search(sctx, view.Token, in)
	if serr != nil {
		// AD03 parity with the MCP shell: session-level failures route through
		// the manager — a credential rejection latches, and a 404 runs the one
		// CAS'd replacement create so the conversation heals for the next
		// capture; this query still reports its own failure.
		if sig := mgr.OnCallError(ctx, view.Token, serr); sig != nil {
			return d.degradedSig(sig)
		}
		return d.degradedSig(&capture.Signal{Reason: obs.DegradedReasonCalmUnreachable, Detail: serr.Error()})
	}

	if documentOrder {
		if totalHits(res) == 0 {
			_, _ = fmt.Fprintln(d.Stdout, "no chunks at this offset"+sourceNote(*source))
			return 0
		}
		_, _ = fmt.Fprint(d.Stdout, formatDocumentOrder(res, *offset))
		return 0
	}
	if totalHits(res) == 0 {
		_, _ = fmt.Fprintln(d.Stdout, "no matches"+sourceNote(*source))
		return 0
	}
	_, _ = fmt.Fprint(d.Stdout, formatSearchResults(res))
	return 0
}

func splitSearchArgs(args []string, source *string, offset, limit, budget *int) []string {
	var queries []string
	literal := false
	for _, a := range args {
		if !literal && a == "--" {
			literal = true
			continue
		}
		if literal {
			queries = append(queries, a)
			continue
		}
		switch {
		case *source == "" && strings.HasPrefix(a, "source=calm:"):
			*source = strings.TrimPrefix(a, "source=")
		case *offset == 0 && numericOption(a, "offset=", offset):
		case *limit == 0 && numericOption(a, "limit=", limit):
		case *budget == 0 && numericOption(a, "budget-bytes=", budget):
		default:
			queries = append(queries, a)
		}
	}
	return queries
}

func numericOption(a, prefix string, dst *int) bool {
	if !strings.HasPrefix(a, prefix) {
		return false
	}
	n, err := strconv.Atoi(strings.TrimPrefix(a, prefix))
	if err != nil {
		return false
	}
	*dst = n
	return true
}

const (
	documentOrderTruncatedMarker  = "[truncated — raise budget-bytes or use a query for the rest]"
	documentOrderContinuationLine = "more chunks remain — search again with source and offset: "
	maxSearchResultLen            = 8192
)

func formatSearchResults(res calm.SearchResults) string {
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
	return out
}

func formatDocumentOrder(res calm.SearchResults, offset int) string {
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
			b.WriteString(documentOrderTruncatedMarker + "\n")
		}
	}
	if res.NextOffset != nil {
		fmt.Fprintf(&b, "\n%s%d\n", documentOrderContinuationLine, *res.NextOffset)
	}
	return b.String()
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
