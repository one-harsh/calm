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

// presentCapture picks the presentation mode. A deliberately-scoped read always
// presents through the ranged view regardless of slice size — ranged presentation
// is a window into a larger capture, so the fused recall label is informative at
// any size (DESIGN.md's presentation contract). Everything else splits by raw
// size: at or below inlineMaxBytes the raw payload wins label-less (summary
// chrome would cost more context than the content); above it, the compact rep +
// fused recall label. `recall` is the shell's retrieval-command name fused into
// the recall hint.
func presentCapture(ctx context.Context, sum calm.IngestSummary, raw string, r exec.Result, token string, rangedView bool, recall string) string {
	if rangedView {
		logging.BindSummary(ctx, obs.PresentationModeFieldRanged)
		return formatRanged(sum, raw, token, recall)
	}
	if len(raw) <= inlineMaxBytes {
		logging.BindSummary(ctx, obs.PresentationModeFieldInline)
		return formatInline(raw, r)
	}
	return formatCompact(sum, r, token, recall)
}

// formatInline is inline mode: the raw payload verbatim with minimal framing — no
// source label, no recall hint. The trailer appears only when it carries signal
// (nonzero exit, timeout, truncation) or when raw is empty (so the response is
// never blank text).
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

// fuseSource fuses the per-call staleness token into the source label per LABELING.md;
// a token-less capture keeps the bare base label.
func fuseSource(source, token string) string {
	if token == "" {
		return source
	}
	return source + "@" + token
}

// writeCaptureLabel emits the recall header shared by the large-output presentations,
// so the fused source label — the agent's addressable way back to the full capture —
// rides on every summary-family response. `recall` names the shell's retrieval command.
func writeCaptureLabel(b *strings.Builder, sum calm.IngestSummary, fusedSource, recall string) {
	fmt.Fprintf(b, "Captured %d/%d sections under %q.\n", sum.SectionsIndexed, sum.SectionsTotal, fusedSource)
	fmt.Fprintf(b, "Retrieve full output: %s source=%s\n", recall, fusedSource)
}

// formatRanged presents a deliberately-scoped calm_read_file slice: the requested
// lines verbatim, never a summary — summarizing a range the agent already narrowed
// would defeat the scoping (content-fidelity). The slice is capped at rangedMaxBytes;
// past the cap a rune-safe prefix ends in a marker naming both recoveries — narrow the
// range, or reread the full capture in document order — and the fused recall label
// always rides along so retrieval identity survives.
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
