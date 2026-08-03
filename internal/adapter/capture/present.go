// Copyright 2026 The CALM Authors
// SPDX-License-Identifier: Apache-2.0

package capture

import (
	"context"
	"fmt"
	"strings"

	logging "github.com/one-harsh/context-logging"

	"github.com/one-harsh/calm/internal/adapter/calm"
	"github.com/one-harsh/calm/internal/adapter/exec"
	"github.com/one-harsh/calm/internal/adapter/obs"
)

const (
	maxCompactSections = 5
	maxCompactLen      = 4096
	inlineMaxBytes     = 512
	rangedMaxBytes     = 8192
)

type presentOptions struct {
	recall        string
	discoveryCard bool
}

func present(ctx context.Context, log *logging.Logger, d Delivery, spec Spec, seq int64, opts presentOptions) Outcome {
	token := d.Unit.Plan.Token
	anyFailed := false
	for _, o := range d.Outcomes {
		if !o.Persisted {
			anyFailed = true
			break
		}
	}

	var out Outcome
	switch {
	case d.Summary == nil:
		logging.BindSummary(ctx, obs.PresentationModeFieldInline)
		log.WithContext(ctx).Warn("all ingests failed; returning raw output",
			obs.DegradedReasonFieldCaptureFailed)
		return Outcome{Visible: spec.Visible, Reason: obs.DegradedReasonCaptureFailed}
	case anyFailed:
		logging.BindSummary(ctx, logging.BoolField(obs.KeyCaptured, true), obs.SourceLabel(d.Summary.Source))
		out = Outcome{
			Visible:     presentCapture(ctx, *d.Summary, spec.Visible, spec.Res, token, spec.RangedView, opts.recall),
			Captured:    true,
			Source:      d.Summary.Source,
			Reason:      obs.DegradedReasonCapturePartial,
			Label:       fuseSource(d.Summary.Source, token),
			FeedbackRef: d.Summary.CorrelationID,
		}
	default:
		logging.BindSummary(ctx, logging.BoolField(obs.KeyCaptured, true), obs.SourceLabel(d.Summary.Source))
		out = Outcome{
			Visible:     presentCapture(ctx, *d.Summary, spec.Visible, spec.Res, token, spec.RangedView, opts.recall),
			Captured:    true,
			Source:      d.Summary.Source,
			Label:       fuseSource(d.Summary.Source, token),
			FeedbackRef: d.Summary.CorrelationID,
		}
	}

	if out.Captured && d.Summary.CorrelationID != "" {
		logging.BindSummary(ctx, obs.CorrelationID(d.Summary.CorrelationID))
	}

	// Persisted sequence, unlike process state, survives capture-shell invocations.
	if opts.discoveryCard && seq == 1 && out.Captured {
		out.Visible = withDiscoveryCard(out.Visible, opts.recall)
	}
	return out
}

// A ranged read stays verbatim and labeled at every size; summarizing an already
// scoped read would violate content-fidelity. Other captures optimize by size.
func presentCapture(ctx context.Context, sum calm.IngestSummary, raw string, r exec.Result, token string, rangedView bool, recall string) string {
	if rangedView {
		logging.BindSummary(ctx, obs.PresentationModeFieldRanged)
		return formatRanged(sum, raw, token, recall)
	}
	if len(raw) <= inlineMaxBytes {
		logging.BindSummary(ctx, obs.PresentationModeFieldInline)
		return formatInline(raw, r)
	}
	logging.BindSummary(ctx, obs.PresentationModeFieldSummary)
	return formatCompact(sum, r, token, recall)
}

// Successful non-empty output stays byte-identical; framing appears only when it
// carries execution state or prevents a blank result.
func formatInline(raw string, r exec.Result) string {
	if r.ExitCode == 0 && !r.TimedOut && !r.Truncated && raw != "" {
		return raw
	}
	var b strings.Builder
	b.WriteString(raw)
	if raw != "" && !strings.HasSuffix(raw, "\n") {
		b.WriteString("\n")
	}
	fmt.Fprintf(&b, "exit=%d", r.ExitCode)
	if r.TimedOut {
		b.WriteString(" (timed out)")
	}
	if r.Truncated {
		b.WriteString(" (output truncated)")
	}
	return b.String()
}

func fuseSource(source, token string) string {
	if token == "" {
		return source
	}
	return source + "@" + token
}

// Summary-family responses must preserve the address back to full content.
func writeCaptureLabel(b *strings.Builder, sum calm.IngestSummary, fusedSource, recall string) {
	fmt.Fprintf(b, "Captured %d/%d sections under %q.\n", sum.SectionsIndexed, sum.SectionsTotal, fusedSource)
	fmt.Fprintf(b, "Retrieve full output: %s source=%s\n", recall, fusedSource)
}

// The byte cap must preserve UTF-8 and name both recovery paths while retaining
// the full capture's address.
func formatRanged(sum calm.IngestSummary, raw string, token, recall string) string {
	var b strings.Builder
	fusedSource := fuseSource(sum.Source, token)
	if len(raw) > rangedMaxBytes {
		b.WriteString(strings.ToValidUTF8(raw[:rangedMaxBytes], ""))
		fmt.Fprintf(&b, "\n… [ranged view capped at %d bytes — narrow start_line/end_line, or reread the full capture in document order with %s source=%s]\n", rangedMaxBytes, recall, fusedSource)
	} else {
		b.WriteString(raw)
		if !strings.HasSuffix(raw, "\n") {
			b.WriteString("\n")
		}
	}
	writeCaptureLabel(&b, sum, fusedSource, recall)
	return b.String()
}

func formatCompact(sum calm.IngestSummary, r exec.Result, token, recall string) string {
	var b strings.Builder
	writeCaptureLabel(&b, sum, fuseSource(sum.Source, token), recall)

	shown := sum.Sections
	if len(shown) > maxCompactSections {
		shown = shown[:maxCompactSections]
	}
	for _, sec := range shown {
		if sec.Preview != "" {
			fmt.Fprintf(&b, "- %s: %s\n", sec.Title, sec.Preview)
		} else {
			fmt.Fprintf(&b, "- %s\n", sec.Title)
		}
	}
	if more := len(sum.Sections) - len(shown); more > 0 {
		fmt.Fprintf(&b, "… +%d more sections\n", more)
	}

	fmt.Fprintf(&b, "exit=%d", r.ExitCode)
	if r.TimedOut {
		b.WriteString(" (timed out)")
	}
	if r.Truncated {
		b.WriteString(" (output truncated)")
	}

	out := b.String()
	if len(out) > maxCompactLen {
		out = strings.ToValidUTF8(out[:maxCompactLen], "") + "…"
	}
	return out
}
