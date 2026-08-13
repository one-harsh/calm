// Copyright 2026 The CALM Authors
// SPDX-License-Identifier: Apache-2.0

package capture

import (
	"context"
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

func TestPresentCapture_ThresholdBoundary(t *testing.T) {
	sum := calm.IngestSummary{Source: "calm:v1:shell:seq#1", SectionsIndexed: 1, SectionsTotal: 1}
	r := exec.Result{ExitCode: 0}
	ctx := context.Background()

	at := strings.Repeat("a", inlineMaxBytes)
	if got := presentCapture(ctx, sum, at, r, "a3f2k6", false, "calm_search", ConsumptionSparse); got != at {
		t.Errorf("raw at threshold must present inline; got %q", got)
	}

	over := strings.Repeat("a", inlineMaxBytes+1)
	got := presentCapture(ctx, sum, over, r, "a3f2k6", false, "calm_search", ConsumptionSparse)
	if !strings.Contains(got, "Captured 1/1 sections under") {
		t.Errorf("raw over threshold must present the compact rep; got %q", got)
	}
	if !strings.Contains(got, "calm_search source=calm:v1:shell:seq#1@a3f2k6") {
		t.Errorf("summary mode must advertise the fused recall label; got %q", got)
	}
}

func TestPresentCapture_WholeConsumptionFloor(t *testing.T) {
	sum := calm.IngestSummary{Source: "calm:v1:vcs:git:status#1", SectionsIndexed: 1, SectionsTotal: 1}
	r := exec.Result{ExitCode: 0}
	ctx := context.Background()

	// Between the two floors: sparse digests, whole stays verbatim.
	mid := strings.Repeat("a", inlineMaxBytes+1)
	if got := presentCapture(ctx, sum, mid, r, "a3f2k6", false, "calm_search", ConsumptionSparse); !strings.Contains(got, "Retrieve full output:") {
		t.Errorf("sparse over its floor must digest; got %q", got)
	}
	whole := presentCapture(ctx, sum, mid, r, "a3f2k6", false, "calm_search", ConsumptionWhole)
	if !strings.HasPrefix(whole, mid) {
		t.Errorf("whole-consumption under its floor must present verbatim; got %q", whole)
	}
	if !strings.Contains(whole, `Captured 1/1 sections under "calm:v1:vcs:git:status#1@a3f2k6".`) {
		t.Errorf("whole-consumption verbatim must carry the compact address; got %q", whole)
	}
	if strings.Contains(whole, "Retrieve full output:") {
		t.Errorf("nothing was withheld — the retrieval-command line must not appear; got %q", whole)
	}

	// At the floor: still verbatim.
	at := strings.Repeat("b", wholeInlineMaxBytes)
	if got := presentCapture(ctx, sum, at, r, "a3f2k6", false, "calm_search", ConsumptionWhole); !strings.HasPrefix(got, at) {
		t.Errorf("whole-consumption at its floor must present verbatim; got len %d", len(got))
	}

	// One byte over: compact digest with the full recall label.
	over := strings.Repeat("c", wholeInlineMaxBytes+1)
	got := presentCapture(ctx, sum, over, r, "a3f2k6", false, "calm_search", ConsumptionWhole)
	if strings.HasPrefix(got, over) {
		t.Errorf("whole-consumption over its floor must digest, not present verbatim")
	}
	if !strings.Contains(got, "Retrieve full output: calm_search source=calm:v1:vcs:git:status#1@a3f2k6") {
		t.Errorf("digest must advertise the full recall label; got %q", got)
	}

	// A truncated-but-successful raw in the whole band must still carry the
	// execution-state framing — verbatim-with-address must not read as complete.
	trunc := presentCapture(ctx, sum, mid, exec.Result{ExitCode: 0, Truncated: true}, "a3f2k6", false, "calm_search", ConsumptionWhole)
	if !strings.HasPrefix(trunc, mid) {
		t.Errorf("truncated whole-band output must stay verbatim; got %q", trunc)
	}
	if !strings.Contains(trunc, "(output truncated)") {
		t.Errorf("truncated whole-band output must carry the truncation marker; got %q", trunc)
	}
}

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
	if got := presentCapture(ctx, sum, small, r, "a3f2k6", true, "calm_search", ConsumptionWhole); got != wantSmall {
		t.Errorf("small ranged view = %q; want slice + label lines %q", got, wantSmall)
	}

	// Over the inline threshold, under the ranged cap: slice verbatim + label line,
	// no compact section chrome.
	slice := strings.Repeat("line\n", 200) // 1000 bytes: > inlineMaxBytes, < rangedMaxBytes
	got := presentCapture(ctx, sum, slice, r, "a3f2k6", true, "calm_search", ConsumptionWhole)
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
	got = presentCapture(ctx, sum, over, r, "a3f2k6", true, "calm_search", ConsumptionWhole)
	if !strings.Contains(got, "ranged view capped at 8192 bytes — narrow start_line/end_line, or reread the full capture in document order with calm_search source=calm:v1:file:read:data.json@a3f2k6") {
		t.Errorf("over-cap ranged view must carry the literal truncation marker; got:\n%s", got)
	}
	if len(got) <= rangedMaxBytes {
		t.Errorf("over-cap ranged view should retain rangedMaxBytes of prefix; got len %d", len(got))
	}
}

func TestPresent_SmallPartial_InlineWithSignal(t *testing.T) {
	outcomes := []extract.WriteOutcome{
		{Source: "calm:v1:vcs:git:status#1", Persisted: true},
		{Source: "calm:v1:vcs:git:status", Persisted: false},
	}
	rep := &calm.IngestSummary{Source: "calm:v1:vcs:git:status#1", SectionsIndexed: 1, SectionsTotal: 1}
	d := Delivery{
		Unit:     CaptureUnit{Plan: extract.Plan{Token: "a3f2k6"}},
		Outcomes: outcomes,
		Summary:  rep,
	}

	out := present(context.Background(), logging.Nop(), d, Spec{Visible: "tiny status\n", Res: exec.Result{}}, 2, presentOptions{recall: "calm_search"})

	if out.Reason != obs.DegradedReasonCapturePartial {
		t.Fatalf("reason = %q; want capture_partial", out.Reason)
	}
	if out.Visible != "tiny status\n" {
		t.Errorf("visible = %q; want raw verbatim (inline-partial)", out.Visible)
	}
	if !out.Captured || out.Source != "calm:v1:vcs:git:status#1" {
		t.Errorf("captured=%v source=%q; want captured under the persisted history source", out.Captured, out.Source)
	}
}

func TestPresentCapture_FailureVerbatim(t *testing.T) {
	sum := calm.IngestSummary{Source: "calm:v1:shell:sh#7", SectionsIndexed: 1, SectionsTotal: 1}
	ctx := context.Background()

	small := "FAIL TestFoo\n  expected 1 got 2\n"
	got := presentCapture(ctx, sum, small, exec.Result{ExitCode: 1}, "a3f2k6", false, "calm_search", ConsumptionWhole)
	if !strings.HasPrefix(got, small) {
		t.Errorf("small failure must present verbatim; got %q", got)
	}
	if !strings.Contains(got, "exit=1") {
		t.Errorf("failure must carry the exit trailer; got %q", got)
	}
	if !strings.Contains(got, `Captured 1/1 sections under "calm:v1:shell:sh#7@a3f2k6".`) {
		t.Errorf("small failure must carry the compact address; got %q", got)
	}
	if strings.Contains(got, "Retrieve full output:") {
		t.Errorf("nothing withheld on a whole verbatim failure — no retrieval line; got %q", got)
	}

	// Larger than the whole-consumption floor but under the failure cap: on a
	// success this would digest; as a failure it stays whole and verbatim.
	medium := "head marker\n" + strings.Repeat("x", wholeInlineMaxBytes+2000) + "\nTAIL FAILURE MARKER\n"
	gotMed := presentCapture(ctx, sum, medium, exec.Result{ExitCode: 2}, "a3f2k6", false, "calm_search", ConsumptionWhole)
	if !strings.Contains(gotMed, "head marker") || !strings.Contains(gotMed, "TAIL FAILURE MARKER") {
		t.Errorf("under-cap failure must present whole verbatim; head/tail missing:\n%s", tail(gotMed, 120))
	}
	if strings.Contains(gotMed, "bytes elided") {
		t.Errorf("under-cap failure must not elide")
	}

	// Over the failure cap: head + tail survive, middle elided with the label.
	head := "HEAD START MARKER\n" + strings.Repeat("h", failHeadBytes)
	middle := strings.Repeat("m", 5000)
	tailPart := strings.Repeat("t", failTailBytes) + "\nTAIL END FAILURE\n"
	huge := head + middle + tailPart
	gotHuge := presentCapture(ctx, sum, huge, exec.Result{ExitCode: 1}, "a3f2k6", false, "calm_search", ConsumptionWhole)
	if !strings.Contains(gotHuge, "HEAD START MARKER") {
		t.Errorf("huge failure must retain the head")
	}
	if !strings.Contains(gotHuge, "TAIL END FAILURE") {
		t.Errorf("huge failure must retain the tail where failures cluster")
	}
	if strings.Contains(gotHuge, middle) {
		t.Errorf("huge failure must elide the middle, not include it whole")
	}
	if !strings.Contains(gotHuge, "bytes elided") {
		t.Errorf("huge failure must mark the elision; got tail:\n%s", tail(gotHuge, 200))
	}
	if !strings.Contains(gotHuge, "Retrieve full output: calm_search source=calm:v1:shell:sh#7@a3f2k6") {
		t.Errorf("elided failure must carry the full recall label; got tail:\n%s", tail(gotHuge, 200))
	}
	if !strings.Contains(gotHuge, "exit=1") {
		t.Errorf("elided failure must still carry the exit trailer")
	}
}
