// Copyright 2026 The CALM Authors
// SPDX-License-Identifier: Apache-2.0

package mcp

import (
	"strings"
	"testing"

	"github.com/one-harsh/calm/internal/adapter/calm"
	"github.com/one-harsh/calm/internal/adapter/exec"
)

func TestFormatCompact_IncludesHandleSectionsTermsAndExit(t *testing.T) {
	sum := calm.IngestSummary{
		Source:          "calm:v1:file:read:foo.go",
		SectionsIndexed: 2,
		SectionsTotal:   2,
		Sections: []calm.SectionPreview{
			{Title: "func main", Preview: "entry point"},
			{Title: "imports"}, // no preview → title-only line
		},
		DistinctiveTerms: []string{"goroutine", "channel"},
	}
	out := formatCompact(sum, exec.Result{ExitCode: 0})

	for _, want := range []string{
		"calm_search source=calm:v1:file:read:foo.go",
		"- func main: entry point",
		"- imports",
		"Terms: goroutine, channel",
		"exit=0",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("compact rep missing %q; got:\n%s", want, out)
		}
	}
}

func TestFormatCompact_CapsSectionsAndNotesOverflow(t *testing.T) {
	var sections []calm.SectionPreview
	for i := 0; i < maxCompactSections+3; i++ {
		sections = append(sections, calm.SectionPreview{Title: "s"})
	}
	out := formatCompact(calm.IngestSummary{Source: "s", Sections: sections}, exec.Result{})
	if !strings.Contains(out, "+3 more sections") {
		t.Errorf("expected overflow note for capped sections; got:\n%s", out)
	}
}

func TestFormatCompact_NotesTimedOutAndTruncated(t *testing.T) {
	out := formatCompact(calm.IngestSummary{Source: "s"}, exec.Result{ExitCode: -1, TimedOut: true, Truncated: true})
	if !strings.Contains(out, "(timed out)") || !strings.Contains(out, "(output truncated)") {
		t.Errorf("expected timed-out and truncated notes; got:\n%s", out)
	}
}

func TestFormatCompact_BoundsTotalLength(t *testing.T) {
	huge := strings.Repeat("x", maxCompactLen+1000)
	out := formatCompact(calm.IngestSummary{
		Source:   "s",
		Sections: []calm.SectionPreview{{Title: "big", Preview: huge}},
	}, exec.Result{})
	if !strings.HasSuffix(out, "…") {
		t.Errorf("over-length rep should end with an ellipsis; got tail %q", tail(out, 8))
	}
	if len(out) > maxCompactLen+len("…") {
		t.Errorf("rep length = %d; want bounded to %d", len(out), maxCompactLen+len("…"))
	}
}

func tail(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[len(s)-n:]
}
