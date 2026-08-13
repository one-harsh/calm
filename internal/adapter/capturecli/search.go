// Copyright 2026 The CALM Authors
// SPDX-License-Identifier: Apache-2.0

package capturecli

import (
	"context"
	"flag"
	"fmt"
	"strconv"
	"strings"
	"time"

	logging "github.com/one-harsh/context-logging"

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
	budget := fs.Int("budget-bytes", 0, "response byte budget (document-order rereads default to a full page)")
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

	start := time.Now()
	ctx = withCallSummary(ctx)
	defer func() {
		d.Logger.SummaryWithContext(ctx).Info(
			"search completed",
			obs.CallDurationMs(time.Since(start).Milliseconds()),
		)
	}()

	mgr, err := d.manager(sessionIDOr(*sessionID))
	if err != nil {
		return d.degradedStderr(ctx, obs.DegradedReasonCaptureFailed)
	}
	view, err := mgr.View(ctx)
	if err != nil {
		return d.degradedStderr(ctx, obs.DegradedReasonCaptureFailed)
	}
	if view.AuthFailed {
		return d.degradedStderr(ctx, obs.DegradedReasonAuthFailed)
	}
	if !view.Established {
		// No session means no corpus; this is an honest empty result, not backend failure.
		n, _ := fmt.Fprintln(d.Stdout, "no captures recorded in this conversation yet")
		logging.BindSummary(ctx, obs.ResponseVisibleBytes(n))
		return 0
	}
	if view.Token == "" {
		return d.degradedStderr(ctx, obs.DegradedReasonSessionLost)
	}

	// Fused-token validation keeps stale content distinct from backend failure.
	calmSource := *source
	if *source != "" {
		stripped, ok := view.Registry.ValidateAndStrip(*source)
		if !ok {
			return d.degradedStderr(ctx, obs.DegradedReasonSessionLost)
		}
		calmSource = stripped
	}

	in := calm.SearchInput{Queries: queries, Source: calmSource, Limit: *limit, BudgetBytes: *budget}
	if documentOrder {
		in.Offset = *offset
		if *budget == 0 {
			in.BudgetBytes = calm.DocumentOrderBudgetDefault
		}
	}
	sctx, cancel := context.WithTimeout(ctx, opTimeout)
	defer cancel()
	res, serr := d.Client.Search(sctx, view.Token, in)
	if serr != nil {
		// AD03: this query reports failure while the manager heals subsequent calls.
		if sig := mgr.OnCallError(ctx, view.Token, serr); sig != nil {
			return d.degradedSig(ctx, sig)
		}
		return d.degradedSig(ctx, &capture.Signal{Reason: obs.DegradedReasonCalmUnreachable, Detail: serr.Error()})
	}
	if res.CorrelationID != "" {
		logging.BindSummary(ctx, obs.CorrelationID(res.CorrelationID))
	}

	var visible string
	if documentOrder {
		visible = capture.FormatDocumentOrder(res, *offset, *source, searchVocab)
	} else {
		visible = capture.FormatSearchResults(res, *source, searchVocab)
	}
	n, _ := fmt.Fprint(d.Stdout, visible)
	logging.BindSummary(ctx, obs.ResponseVisibleBytes(n), obs.ResponseRawBytes(n))
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

// The capture shell must make feedback discoverable without a tool description.
var searchVocab = capture.SearchVocab{
	TruncatedMarker:  "[truncated — raise budget-bytes or use a query for the rest]",
	ContinuationLine: "more chunks remain — search again with source and offset: ",
	FeedbackPrefix:   "↳ feedback: calm-capture feedback ",
	ZeroHitRanked:    "no matches",
	ZeroHitDocument:  "no chunks at this offset",
}
