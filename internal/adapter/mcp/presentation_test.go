// Copyright 2026 The CALM Authors
// SPDX-License-Identifier: Apache-2.0

package mcp

import (
	"context"
	"errors"
	"strings"
	"testing"

	logging "github.com/one-harsh/context-logging"

	"github.com/one-harsh/calm/internal/adapter/calm"
	"github.com/one-harsh/calm/internal/adapter/exec"
	"github.com/one-harsh/calm/internal/adapter/extract"
	"github.com/one-harsh/calm/internal/adapter/obs"
)

func TestFormatInline(t *testing.T) {
	cases := []struct {
		name string
		raw  string
		r    exec.Result
		want string
	}{
		{
			name: "clean exit is raw verbatim, no framing",
			raw:  "hello\n",
			r:    exec.Result{ExitCode: 0},
			want: "hello\n",
		},
		{
			name: "nonzero exit appends trailer",
			raw:  "boom\n",
			r:    exec.Result{ExitCode: 3},
			want: "boom\nexit=3",
		},
		{
			name: "trailer gets its own line when raw lacks a newline",
			raw:  "boom",
			r:    exec.Result{ExitCode: 3},
			want: "boom\nexit=3",
		},
		{
			name: "timed out and truncated markers",
			raw:  "partial\n",
			r:    exec.Result{ExitCode: 1, TimedOut: true, Truncated: true},
			want: "partial\nexit=1 (timed out) (output truncated)",
		},
		{
			name: "empty raw emits the trailer so the response is never blank",
			raw:  "",
			r:    exec.Result{ExitCode: 0},
			want: "exit=0",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := formatInline(c.raw, c.r); got != c.want {
				t.Errorf("formatInline = %q; want %q", got, c.want)
			}
		})
	}
}

// The mode boundary: raw at inlineMaxBytes presents inline; one byte over
// presents the compact rep with the fused recall label.
func TestPresentCapture_ThresholdBoundary(t *testing.T) {
	sum := calm.IngestSummary{Source: "calm:v1:shell:seq#1", SectionsIndexed: 1, SectionsTotal: 1}
	r := exec.Result{ExitCode: 0}
	ctx := context.Background()

	at := strings.Repeat("a", inlineMaxBytes)
	if got := presentCapture(ctx, sum, at, r, "a3f2k6", false); got != at {
		t.Errorf("raw at threshold must present inline; got %q", got)
	}

	over := strings.Repeat("a", inlineMaxBytes+1)
	got := presentCapture(ctx, sum, over, r, "a3f2k6", false)
	if !strings.Contains(got, "Captured 1/1 sections under") {
		t.Errorf("raw over threshold must present the compact rep; got %q", got)
	}
	if !strings.Contains(got, "calm_search source=calm:v1:shell:seq#1@a3f2k6") {
		t.Errorf("summary mode must advertise the fused recall label; got %q", got)
	}
}

// A ranged read presents its slice verbatim at any size — small slices included —
// and always carries the fused recall label: ranged presentation is a window into
// a larger capture, so the label is informative even below the inline threshold.
// Past the byte cap the slice becomes a rune-safe prefix plus a truncation marker.
func TestPresentCapture_RangedView(t *testing.T) {
	sum := calm.IngestSummary{Source: "calm:v1:file:read:data.json", SectionsIndexed: 3, SectionsTotal: 3}
	r := exec.Result{ExitCode: 0}
	ctx := context.Background()

	// At or below the inline threshold: slice verbatim + label lines, never the
	// label-less inline path.
	small := "alpha\nbeta\n"
	wantSmall := small +
		"Captured 3/3 sections under \"calm:v1:file:read:data.json@a3f2k6\".\n" +
		"Retrieve full output: calm_search source=calm:v1:file:read:data.json@a3f2k6\n"
	if got := presentCapture(ctx, sum, small, r, "a3f2k6", true); got != wantSmall {
		t.Errorf("small ranged view = %q; want slice + label lines %q", got, wantSmall)
	}

	// Over the inline threshold, under the ranged cap: slice verbatim + label line,
	// no compact section chrome.
	slice := strings.Repeat("line\n", 200) // 1000 bytes: > inlineMaxBytes, < rangedMaxBytes
	got := presentCapture(ctx, sum, slice, r, "a3f2k6", true)
	if !strings.Contains(got, slice) {
		t.Errorf("ranged view must present the slice verbatim; got:\n%s", got)
	}
	if !strings.Contains(got, "Captured 3/3 sections under") ||
		!strings.Contains(got, "calm_search source=calm:v1:file:read:data.json@a3f2k6") {
		t.Errorf("ranged view must carry the fused recall label; got:\n%s", got)
	}

	// Over the ranged cap: rune-safe prefix + the literal truncation marker naming
	// both recoveries.
	over := strings.Repeat("z", rangedMaxBytes+500)
	got = presentCapture(ctx, sum, over, r, "a3f2k6", true)
	if !strings.Contains(got, "ranged view capped at 8192 bytes — narrow start_line/end_line, or reread the full capture in document order with calm_search source=calm:v1:file:read:data.json@a3f2k6") {
		t.Errorf("over-cap ranged view must carry the literal truncation marker; got:\n%s", got)
	}
	if len(got) <= rangedMaxBytes {
		t.Errorf("over-cap ranged view should retain rangedMaxBytes of prefix; got len %d", len(got))
	}
}

// Uniform threshold: capture_partial with a small raw presents inline (raw
// verbatim, no label in visible text) while still signaling capture_partial —
// degradation and presentation are orthogonal dimensions.
func TestFormatCaptureOutcome_SmallPartial_InlineWithSignal(t *testing.T) {
	s := NewServer(Config{Logger: logging.Nop(), SessionTTLMinutes: 60})
	outcomes := []extract.WriteOutcome{
		{Source: "calm:v1:vcs:git:status#1", Persisted: true},
		{Source: "calm:v1:vcs:git:status", Persisted: false},
	}
	rep := &calm.IngestSummary{Source: "calm:v1:vcs:git:status#1", SectionsIndexed: 1, SectionsTotal: 1}

	res, err := s.formatCaptureOutcome(context.Background(), outcomes, rep, "tiny status\n", exec.Result{}, "a3f2k6", false)

	var deg *DegradedSignal
	if !errors.As(err, &deg) || deg.Reason != obs.DegradedReasonCapturePartial {
		t.Fatalf("err = %v; want capture_partial DegradedSignal", err)
	}
	if got := res.Content[0].Text; got != "tiny status\n" {
		t.Errorf("text = %q; want raw verbatim (inline-partial)", got)
	}
}
