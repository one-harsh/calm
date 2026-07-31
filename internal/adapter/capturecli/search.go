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
	if !view.Established {
		// A never-established conversation has an empty corpus, not a degraded
		// backend: report an honest empty result and never reach CALM.
		_, _ = fmt.Fprintln(d.Stdout, "no captures recorded in this conversation yet")
		return 0
	}
	if view.Token == "" {
		return d.degradedStderr(obs.DegradedReasonSessionLost)
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
		_, _ = fmt.Fprint(d.Stdout, capture.FormatDocumentOrder(res, *offset, *source, searchVocab))
		return 0
	}
	_, _ = fmt.Fprint(d.Stdout, capture.FormatSearchResults(res, *source, searchVocab))
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

// searchVocab is the capture shell's search-presentation vocabulary; its
// FeedbackPrefix appends the correlation id as the handle `calm-capture
// feedback` accepts.
var searchVocab = capture.SearchVocab{
	TruncatedMarker:  "[truncated — raise budget-bytes or use a query for the rest]",
	ContinuationLine: "more chunks remain — search again with source and offset: ",
	FeedbackPrefix:   "↳ feedback: calm-capture feedback ",
	ZeroHitRanked:    "no matches",
	ZeroHitDocument:  "no chunks at this offset",
}
